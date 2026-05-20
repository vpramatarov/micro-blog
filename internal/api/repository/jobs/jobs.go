package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const DB_TABLE string = "jobs"

// ErrNoJob is what Claim returns when the queue is empty. Not a real error — the worker treats it as "sleep and try again".
var ErrNoJob = errors.New("jobs: no pending job")

// Job is the row model of the `jobs` table.
type Job struct {
	ID        int64
	Type      string
	Payload   []byte
	Attempts  int
	LastError string
}

// Repo wraps *sql.DB for the `jobs` table.
type Repo struct {
	db *sql.DB
}

func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// Enqueue inserts a new pending job. Returns the job id.
func (r *Repo) Enqueue(ctx context.Context, jobType string, payload []byte) (int64, error) {
	q := fmt.Sprintf(`INSERT INTO %s (type, payload, status) VALUES (?, ?, 'pending')`, DB_TABLE)
	res, err := r.db.ExecContext(ctx, q, jobType, string(payload))
	if err != nil {
		return 0, fmt.Errorf("jobs: enqueue %q: %w", jobType, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("jobs: last insert id: %w", err)
	}

	return id, nil
}

// Claim atomically flips the oldest pending job to 'running' and returns it.
// Returns ErrNoJob when the queue is empty.
func (r *Repo) Claim(ctx context.Context) (*Job, error) {
	q := fmt.Sprintf(`
        UPDATE %[1]s
        SET status = 'running',
            attempts = attempts + 1,
            updated_at = CURRENT_TIMESTAMP
        WHERE id = (
            SELECT id FROM %[1]s
            WHERE status = 'pending'
            ORDER BY id ASC
            LIMIT 1
        )
        RETURNING id, type, payload, attempts`,
		DB_TABLE)
	var j Job
	var payload string
	err := r.db.QueryRowContext(ctx, q).Scan(&j.ID, &j.Type, &payload, &j.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoJob
	}

	if err != nil {
		return nil, fmt.Errorf("jobs: claim: %w", err)
	}

	j.Payload = []byte(payload)
	return &j, nil
}

// MarkDone flips a running job to 'done'.
func (r *Repo) MarkDone(ctx context.Context, id int64) error {
	q := fmt.Sprintf(`UPDATE %s SET status='done', last_error=NULL, updated_at=CURRENT_TIMESTAMP WHERE id=?`, DB_TABLE)
	if _, err := r.db.ExecContext(ctx, q, id); err != nil {
		return fmt.Errorf("jobs: mark done %d: %w", id, err)
	}

	return nil
}

// MarkFailed flips a running job to 'failed' with the error stored for debugging.
func (r *Repo) MarkFailed(ctx context.Context, id int64, errMsg string) error {
	q := fmt.Sprintf(`UPDATE %s SET status='failed', last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, DB_TABLE)
	if _, err := r.db.ExecContext(ctx, q, errMsg, id); err != nil {
		return fmt.Errorf("jobs: mark failed %d: %w", id, err)
	}

	return nil
}

// Requeue flips a running job back to 'pending' (used when the handler returned an error but the attempt budget isn't exhausted yet).
// The attempts counter was already incremented by Claim.
func (r *Repo) Requeue(ctx context.Context, id int64, errMsg string) error {
	q := fmt.Sprintf(`UPDATE %s SET status='pending', last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, DB_TABLE)
	if _, err := r.db.ExecContext(ctx, q, errMsg, id); err != nil {
		return fmt.Errorf("jobs: requeue %d: %w", id, err)
	}

	return nil
}

// ResetStuckRunning is called once at startup to recover from a worker crash.
// Any row left in 'running' (which can only happen if the process died between Claim and MarkDone/MarkFailed/Requeue) is flipped back to 'pending'.
// The attempts counter is preserved — if it's already at the cap, the next handler error sends it to 'failed' as usual.
func (r *Repo) ResetStuckRunning(ctx context.Context) (int64, error) {
	q := fmt.Sprintf(`UPDATE %s SET status='pending', updated_at=CURRENT_TIMESTAMP WHERE status='running'`, DB_TABLE)
	res, err := r.db.ExecContext(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("jobs: reset stuck running: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobs: rows affected: %w", err)
	}

	return n, nil
}
