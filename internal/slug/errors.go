package slug

import "errors"

// ErrDuplicate signals that an INSERT or UPDATE violated the UNIQUE constraint on the slug column.
var ErrDuplicate = errors.New("slug: unique constraint violated")

// ErrEmptyGeneratedSlug signals that Generate(name) collapsed to "" — the caller's `name` had no slug-eligible characters (e.g. emoji-only).
var ErrEmptyGeneratedSlug = errors.New("slug: generated slug is empty")
