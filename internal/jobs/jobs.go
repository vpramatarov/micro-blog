// Package jobs implements a tiny DB-backed job queue + in-process worker.
// It exists to keep slow work (e.g. image variant generation) off the HTTP
// request path while still surviving server restarts — pending jobs persist in the `jobs` table.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vpramatarov/micro-blog/internal/api/repository/jobs"
)

// MaxAttempts is the cap on retries for a single job.
const MaxAttempts int = 3

// DefaultPollInterval is how long the worker sleeps between empty polls.
// Tunable per-worker via WithInterval.
const DefaultPollInterval time.Duration = 2 * time.Second

// Handler processes one job. Returns nil on success; any error triggers the worker's retry-or-fail logic.
type Handler func(ctx context.Context, payload []byte) error

// Worker polls Repo, dispatches by Type, retries up to MaxAttempts.
type Worker struct {
	Repo     *jobs.Repo
	handlers map[string]Handler
	log      *slog.Logger
	interval time.Duration
}

// NewWorker returns a Worker with no handlers registered. Use Register to add them before calling Run.
func NewWorker(repo *jobs.Repo, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}

	return &Worker{
		Repo:     repo,
		handlers: map[string]Handler{},
		log:      log,
		interval: DefaultPollInterval,
	}
}

// WithInterval lets callers (mostly tests) override the poll cadence.
func (w *Worker) WithInterval(d time.Duration) *Worker {
	w.interval = d
	return w
}

// Register attaches a handler to a job type. Re-registering replaces the previous handler.
func (w *Worker) Register(jobType string, h Handler) {
	w.handlers[jobType] = h
}

// Run blocks until ctx is canceled. On each tick it claims at most one job, dispatches it,
// and writes the outcome. An empty queue triggers a sleep of `interval`.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("job worker started", "interval", w.interval)
	defer w.log.Info("job worker stopped")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		job, err := w.Repo.Claim(ctx)
		if errors.Is(err, jobs.ErrNoJob) {
			w.sleep(ctx)
			continue
		}

		if err != nil {
			w.log.Error("claim job", "err", err)
			w.sleep(ctx)
			continue
		}

		w.process(ctx, job)
	}
}

func (w *Worker) sleep(ctx context.Context) {
	t := time.NewTimer(w.interval)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (w *Worker) process(ctx context.Context, job *jobs.Job) {
	h, ok := w.handlers[job.Type]
	if !ok {
		w.log.Error("no handler for job type", "type", job.Type, "id", job.ID)
		_ = w.Repo.MarkFailed(ctx, job.ID, "no handler registered for type "+job.Type)
		return
	}

	if err := h(ctx, job.Payload); err != nil {
		w.log.Warn("job handler error", "type", job.Type, "id", job.ID, "attempt", job.Attempts, "err", err)
		if job.Attempts >= MaxAttempts {
			_ = w.Repo.MarkFailed(ctx, job.ID, err.Error())
			return
		}

		_ = w.Repo.Requeue(ctx, job.ID, err.Error())
		return
	}

	if err := w.Repo.MarkDone(ctx, job.ID); err != nil {
		w.log.Error("mark done", "id", job.ID, "err", err)
	}
}
