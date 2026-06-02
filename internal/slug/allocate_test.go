package slug_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/slug"
)

// fakeFinder feeds Allocate a deterministic sequence of candidates so the
// tests can simulate "first attempt's candidate collides, retry sees the next free one."
// A non-nil err field surfaces an empty string + error pair.
type fakeFinder struct {
	candidates []string
	err        error
	calls      int
}

func (f *fakeFinder) GenerateSlug(_ context.Context, _ string, _ int64) (string, error) {
	if f.err != nil {
		return "", f.err
	}

	if f.calls >= len(f.candidates) {
		return "", errors.New("fakeFinder: out of candidates")
	}

	out := f.candidates[f.calls]
	f.calls++
	return out, nil
}

var (
	errOther  = errors.New("other-sentinel")
	errFinder = errors.New("finder-blew-up")
)

func TestAllocateHappyPath(t *testing.T) {
	f := &fakeFinder{candidates: []string{"foo"}}
	created := ""
	got, err := slug.Allocate(context.Background(), f, "foo", "", 0,
		func(s string) (string, error) {
			created = s
			return "id-" + s, nil
		})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got != "id-foo" {
		t.Errorf("got %q, want id-foo", got)
	}

	if created != "foo" {
		t.Errorf("created with %q, want foo", created)
	}

	if f.calls != 1 {
		t.Errorf("finder called %d times, want 1", f.calls)
	}
}

func TestAllocateRetriesOnceOnDup(t *testing.T) {
	f := &fakeFinder{candidates: []string{"foo", "foo-2"}}
	attempts := 0
	got, err := slug.Allocate(context.Background(), f, "foo", "", 0,
		func(s string) (string, error) {
			attempts++
			if attempts == 1 {
				return "", slug.ErrDuplicate // first attempt races with another writer
			}
			return "id-" + s, nil
		})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got != "id-foo-2" {
		t.Errorf("got %q, want id-foo-2 (retry result)", got)
	}

	if attempts != 2 {
		t.Errorf("create called %d times, want 2", attempts)
	}

	if f.calls != 2 {
		t.Errorf("finder called %d times, want 2", f.calls)
	}
}

func TestAllocateGivesUpAfterSecondDup(t *testing.T) {
	f := &fakeFinder{candidates: []string{"foo", "foo-2"}}
	attempts := 0
	got, err := slug.Allocate(context.Background(), f, "foo", "", 0,
		func(_ string) (string, error) {
			attempts++
			return "", slug.ErrDuplicate
		})
	if !errors.Is(err, slug.ErrAllocateRetryExhausted) {
		t.Errorf("err: got %v, want ErrAllocateRetryExhausted", err)
	}

	if got != "" {
		t.Errorf("zero value: got %q, want \"\"", got)
	}

	if attempts != 2 {
		t.Errorf("create called %d times, want 2 (retry budget)", attempts)
	}
}

func TestAllocateSurfacesNonDupCreateError(t *testing.T) {
	f := &fakeFinder{candidates: []string{"foo"}}
	attempts := 0
	got, err := slug.Allocate(context.Background(), f, "foo", "", 0,
		func(_ string) (string, error) {
			attempts++
			return "partial-result", errOther
		})
	if !errors.Is(err, errOther) {
		t.Errorf("err: got %v, want errOther", err)
	}
	// Documents the contract: Allocate returns whatever the closure returned alongside the error, even when the error is fatal.
	// Caller writes the 5xx envelope and discards the partial result.
	if got != "partial-result" {
		t.Errorf("result on non-dup error: got %q, want partial-result", got)
	}

	if attempts != 1 {
		t.Errorf("create called %d times, want 1 (no retry on non-dup)", attempts)
	}
}

func TestAllocateSurfacesFinderError(t *testing.T) {
	f := &fakeFinder{err: errFinder}
	attempts := 0
	got, err := slug.Allocate(context.Background(), f, "foo", "", 0,
		func(_ string) (string, error) {
			attempts++
			return "should-not-run", nil
		})
	if !errors.Is(err, errFinder) {
		t.Errorf("err: got %v, want errFinder", err)
	}

	if got != "" {
		t.Errorf("zero value: got %q, want \"\"", got)
	}

	if attempts != 0 {
		t.Errorf("create called %d times, want 0 (finder failed)", attempts)
	}
}

// TestAllocateRetriesOnWrappedDuplicate verifies that callers wrapping slug.ErrDuplicate
// (e.g. `fmt.Errorf("create category: %w", slug.ErrDuplicate)`) still trigger the retry.
// The contract is "errors.Is(err, ErrDuplicate)", so any wrapped instance must be honored.
func TestAllocateRetriesOnWrappedDuplicate(t *testing.T) {
	f := &fakeFinder{candidates: []string{"foo", "foo-2"}}
	attempts := 0
	_, err := slug.Allocate(context.Background(), f, "foo", "", 0,
		func(s string) (string, error) {
			attempts++
			if attempts == 1 {
				return "", errors.Join(errors.New("create category"), slug.ErrDuplicate)
			}

			return "id-" + s, nil
		})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if attempts != 2 {
		t.Errorf("attempts: got %d, want 2 (wrapped ErrDuplicate must trigger retry)", attempts)
	}
}

// TestAllocatePropagatesContext makes sure the ctx parameter actually reaches the finder —
// the closure doesn't see it, so a regression where Allocate passed context.Background() would silently break cancellation.
func TestAllocatePropagatesContext(t *testing.T) {
	type ctxKey struct{}
	wantCtx := context.WithValue(context.Background(), ctxKey{}, "carry")
	got := ""
	finder := finderFunc(func(ctx context.Context, _ string, _ int64) (string, error) {
		got, _ = ctx.Value(ctxKey{}).(string)
		return "x", nil
	})
	_, err := slug.Allocate(wantCtx, finder, "x", "", 0,
		func(s string) (string, error) { return s, nil })
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got != "carry" {
		t.Errorf("ctx not propagated: got %q, want carry", got)
	}
}

// finderFunc adapts a function literal to slug.SlugInterface so the context-propagation test stays inline.
type finderFunc func(ctx context.Context, base string, excludeID int64) (string, error)

// GenerateSlug implements [slug.SlugInterface].
func (f finderFunc) GenerateSlug(ctx context.Context, base string, excludeID int64) (string, error) {
	return f(ctx, base, excludeID)
}

func TestAllocateForName_ExplicitSlug(t *testing.T) {
	f := &fakeFinder{candidates: []string{"should-not-be-used"}}
	got, err := slug.Allocate(context.Background(), f, "Engineering", "eng", 0, func(s string) (string, error) { return "id-" + s, nil })
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got != "id-eng" {
		t.Errorf("got %q, want id-eng (explicit slug persisted as-is)", got)
	}

	if f.calls != 0 {
		t.Errorf("finder called %d times on explicit-slug path, want 0", f.calls)
	}
}

func TestAllocateForName_AutoGenerate(t *testing.T) {
	f := &fakeFinder{candidates: []string{"engineering"}}
	got, err := slug.Allocate(context.Background(), f, "Engineering", "", 0,
		func(s string) (string, error) {
			return "id-" + s, nil
		})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got != "id-engineering" {
		t.Errorf("got %q, want id-engineering", got)
	}

	if f.calls != 1 {
		t.Errorf("finder calls: got %d, want 1", f.calls)
	}
}

func TestAllocateForName_AutoGenerateBulgarian(t *testing.T) {
	f := &fakeFinder{candidates: []string{"zdravey-svyat"}}
	got, err := slug.Allocate(context.Background(), f, "Здравей свят", "", 0,
		func(s string) (string, error) {
			return "id-" + s, nil
		})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got != "id-zdravey-svyat" {
		t.Errorf("got %q, want id-zdravey-svyat (Bulgarian transliteration)", got)
	}
}

func TestAllocateForName_EmptyGeneratedSlug(t *testing.T) {
	f := &fakeFinder{}
	creates := 0
	got, err := slug.Allocate(context.Background(), f, "🚀🚀🚀", "", 0,
		func(s string) (string, error) {
			creates++
			return "should-not-happen", nil
		})
	if !errors.Is(err, slug.ErrEmptyGeneratedSlug) {
		t.Errorf("err: got %v, want ErrEmptyGeneratedSlug", err)
	}

	if got != "" {
		t.Errorf("zero value: got %q, want \"\"", got)
	}

	if creates != 0 {
		t.Errorf("create called %d times on empty-base path, want 0", creates)
	}

	if f.calls != 0 {
		t.Errorf("finder called %d times on empty-base path, want 0", f.calls)
	}
}

func TestAllocateForName_ExplicitSlugConflictNoRetry(t *testing.T) {
	f := &fakeFinder{candidates: []string{"never-reached"}}
	attempts := 0
	got, err := slug.Allocate(context.Background(), f, "Engineering", "eng", 0,
		func(_ string) (string, error) {
			attempts++
			return "", slug.ErrDuplicate
		})
	if !errors.Is(err, slug.ErrDuplicate) {
		t.Errorf("err: got %v, want ErrDuplicate (no retry on explicit-slug path)", err)
	}

	if got != "" {
		t.Errorf("zero value: got %q, want \"\"", got)
	}

	if attempts != 1 {
		t.Errorf("create called %d times, want 1 (explicit-slug never retries)", attempts)
	}

	if f.calls != 0 {
		t.Errorf("finder called %d times, want 0 (explicit-slug skips finder)", f.calls)
	}
}

func TestAllocateForName_AutoGenerateRetriesOnRace(t *testing.T) {
	f := &fakeFinder{candidates: []string{"engineering", "engineering-2"}}
	attempts := 0
	got, err := slug.Allocate(context.Background(), f, "Engineering", "", 0,
		func(s string) (string, error) {
			attempts++
			if attempts == 1 {
				return "", slug.ErrDuplicate
			}
			return "id-" + s, nil
		})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got != "id-engineering-2" {
		t.Errorf("got %q, want id-engineering-2 (retry result)", got)
	}

	if attempts != 2 {
		t.Errorf("create called %d times, want 2 (auto-generate retries once)", attempts)
	}
}

// TestAllocateForName_ExcludeIDPassedToFinder verifies AllocateForName forwards
// the excludeID argument to FindAvailableSlug — load-bearing for UPDATE paths
// where the row's own slug must be invisible to the collision check.
func TestAllocateForName_ExcludeIDPassedToFinder(t *testing.T) {
	var seenExcludeID int64 = -1
	finder := finderFunc(func(_ context.Context, _ string, excludeID int64) (string, error) {
		seenExcludeID = excludeID
		return "engineering", nil
	})
	_, err := slug.Allocate(context.Background(), finder, "Engineering", "", 42,
		func(s string) (string, error) { return "id-" + s, nil })
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if seenExcludeID != 42 {
		t.Errorf("finder saw excludeID=%d, want 42 (UPDATE path must pass row.id)", seenExcludeID)
	}
}

// TestAllocateForName_ExplicitSlugIgnoresExcludeID — the explicit-slug branch shortcuts before any finder call, so excludeID has no effect there.
// Documents the contract.
func TestAllocateForName_ExplicitSlugIgnoresExcludeID(t *testing.T) {
	finderCalled := false
	finder := finderFunc(func(_ context.Context, _ string, _ int64) (string, error) {
		finderCalled = true
		return "", nil
	})
	got, err := slug.Allocate(context.Background(), finder, "Engineering", "eng", 42,
		func(s string) (string, error) { return "id-" + s, nil })
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got != "id-eng" {
		t.Errorf("got %q, want id-eng", got)
	}

	if finderCalled {
		t.Errorf("finder must NOT be called on explicit-slug branch (even with excludeID set)")
	}
}
