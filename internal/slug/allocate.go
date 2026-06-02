package slug

import (
	"context"
	"errors"
)

// SlugInterface: implement this by exposing a GenerateSlug method that delegates to *Finder.Generate.
type SlugInterface interface {
	GenerateSlug(ctx context.Context, base string, excludeID int64) (string, error)
}

// ErrAllocateRetryExhausted is returned when two consecutive create attempts both lose the SELECT/INSERT race.
var ErrAllocateRetryExhausted = errors.New("slug: race retry exhausted")

// Allocate resolves the slug for a write operation (insert OR update) that takes a human-readable name and an optional client-supplied slug.
//
//   - clientSlug != "": persist the client's value via create(clientSlug).
//     The slug-UNIQUE error surfaces as slug.ErrDuplicate so the caller can ap it to 409 slug_conflict.
//     No retry — the client's choice is never silently mangled.
//     (For UPDATE handlers that want preserve-on-omit semantics,
//     coalesce `desiredSlug := clientSlug || existing.Slug` and pass `desiredSlug` here — the explicit-slug branch fires either way.)
//   - clientSlug == "":  Generate(name); if "" → ErrEmptyGeneratedSlug.
//     Otherwise Allocate (auto-suffix + retry-once on race).
//
// excludeID:
//   - Insert: pass 0. Every existing row's slug counts as a possible conflict.
//   - Update: pass the row's id. The row's own slug is invisible to the
//     finder so it doesn't self-collide.
func Allocate[T any](
	ctx context.Context,
	finder SlugInterface,
	name string,
	clientSlug string,
	excludeID int64,
	create func(slug string) (T, error),
) (T, error) {
	if clientSlug != "" {
		return create(clientSlug)
	}

	var zero T
	base := Generate(name)
	if base == "" {
		return zero, ErrEmptyGeneratedSlug
	}

	for range 2 {
		candidate, err := finder.GenerateSlug(ctx, base, excludeID)
		if err != nil {
			return zero, err
		}

		result, err := create(candidate)
		if err == nil {
			return result, nil
		}

		if !errors.Is(err, ErrDuplicate) {
			return result, err
		}
	}

	return zero, ErrAllocateRetryExhausted
}
