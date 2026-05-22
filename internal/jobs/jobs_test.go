package jobs_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	jobsRepo "github.com/vpramatarov/micro-blog/internal/api/repository/jobs"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// TestEnqueueSignalsNotifications pins the wake-up channel pattern that closes P5:
// Enqueue must do a non-blocking send on Notifications() so a worker selecting on it wakes immediately instead of waiting for the next fallback poll tick.
func TestEnqueueSignalsNotifications(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := jobsRepo.New(db)
	ctx := t.Context()

	if _, err := r.Enqueue(ctx, "image_variants", []byte(`{}`)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// The channel is buffered cap-1 with a non-blocking send, so the signal
	// is sitting there immediately after Enqueue returns. Use a generous
	// timeout to avoid CI flake but it should fire essentially instantly.
	select {
	case <-r.Notifications():
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue did not signal Notifications within 2s")
	}
}

// TestEnqueueNotificationsCoalesce confirms that multiple Enqueue calls before a single read result in only one wake-up.
// Coalescing is fine because the worker's Claim drains the queue greedily on each wake.
func TestEnqueueNotificationsCoalesce(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := jobsRepo.New(db)
	ctx := t.Context()

	for i := range 5 {
		if _, err := r.Enqueue(ctx, "image_variants", []byte(`{}`)); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	// First read should fire immediately.
	select {
	case <-r.Notifications():
	case <-time.After(time.Second):
		t.Fatal("expected at least one signal after 5 enqueues")
	}
	// No more signals queued — capacity 1 + non-blocking send drops extras.
	select {
	case <-r.Notifications():
		t.Error("Notifications fired twice for coalesced enqueues; should be capacity-1")
	case <-time.After(50 * time.Millisecond):
		// Good — no second signal.
	}
}

// TestEnqueueDropsSignalWhenNoListener confirms the non-blocking send
// behaviour: Enqueue must not block when nothing is reading the channel.
// (Tests that only exercise the repo don't construct a worker; without
// non-blocking sends, those tests would deadlock on the second Enqueue.)
func TestEnqueueDropsSignalWhenNoListener(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := jobsRepo.New(db)
	ctx := t.Context()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 10 {
			if _, err := r.Enqueue(ctx, "image_variants", []byte(`{}`)); err != nil {
				t.Errorf("enqueue %d: %v", i, err)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Enqueue appears to block when channel has no listener")
	}
}
