// Package uploads owns the disk layout for user-uploaded files (post featured images)
// All paths returned by this package are RELATIVE to the configured root directory —
// the value stored in the DB and the value served back through /uploads/{path} static routing.
package uploads

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vpramatarov/micro-blog/internal/slug"
)

// maxCollisionSuffix caps how many "name-N.ext" retries we attempt before giving up. better than looping forever.
const maxCollisionSuffix int = 1000

// ErrCollisionExhausted means we couldn't find a free name within maxCollisionSuffix attempts. The handler maps this to 500.
var ErrCollisionExhausted = errors.New("uploads: too many filename collisions")

// Storage roots all reads/writes at a single base directory. In production it's "./uploads" relative to the server's CWD. Tests pass t.TempDir().
type Storage struct {
	root string
}

// New returns a Storage rooted at root. The directory itself is created on first write (os.MkdirAll), so callers don't need to pre-create it.
func New(root string) *Storage {
	return &Storage{root: root}
}

// Root returns the absolute (or as-supplied) root. Used by the static file handler so the two stay in sync.
func (s *Storage) Root() string {
	return s.root
}

// SaveOriginal writes data under root/YYYY/MM/DD/<sanitized-name><ext>.
// The returned relPath uses forward slashes (matching URL conventions) regardless of OS — "2026/02/03/foo.jpg".
func (s *Storage) SaveOriginal(today time.Time, uploadedName, ext string, data []byte) (relativePath string, err error) {
	if ext != ".jpg" && ext != ".png" {
		return "", fmt.Errorf("uploads: unexpected ext %q (expected .jpg or .png)", ext)
	}

	timeBucket := today.UTC().Format("2006/01/02")
	dir := filepath.Join(s.Root(), filepath.FromSlash(timeBucket))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("uploads: mkdir %q: %w", dir, err)
	}

	base := sanitizeBase(uploadedName, today)
	// Try base.ext, then base-2.ext, base-3.ext, ... until O_EXCL succeeds.
	for i := range maxCollisionSuffix {
		name := candidateName(base, i, ext)
		full := filepath.Join(dir, name)
		f, openErr := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if openErr == nil {
			_, writeErr := f.Write(data)
			closeErr := f.Close()
			if writeErr != nil {
				_ = os.Remove(full)
				return "", fmt.Errorf("uploads: write %q: %w", full, writeErr)
			}

			if closeErr != nil {
				_ = os.Remove(full)
				return "", fmt.Errorf("uploads: close %q: %w", full, closeErr)
			}

			return timeBucket + "/" + name, nil
		}

		if !errors.Is(openErr, os.ErrExist) {
			return "", fmt.Errorf("uploads: open %q: %w", full, openErr)
		}
		// Collision — bump the suffix and retry.
	}

	return "", ErrCollisionExhausted
}

// SaveVariant writes a variant alongside the original. relOriginal must be the path SaveOriginal returned ("2026/02/03/my-photo.jpg");
// suffix is "s", "m", or "l".
// The resulting file is "my-photo-s.jpg" (size suffix inserted before the extension).
func (s *Storage) SaveVariant(relOriginal, sizeSuffix string, data []byte) error {
	if sizeSuffix != "s" && sizeSuffix != "m" && sizeSuffix != "l" {
		return fmt.Errorf("uploads: unexpected size suffix %q", sizeSuffix)
	}

	full := s.variantPath(relOriginal, sizeSuffix)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("uploads: mkdir %q: %w", filepath.Dir(full), err)
	}

	if err := os.WriteFile(full, data, 0o644); err != nil {
		return fmt.Errorf("uploads: write variant %q: %w", full, err)
	}

	return nil
}

// ReadOriginal returns the bytes of relOriginal. Used by the worker handler when generating variants.
func (s *Storage) ReadOriginal(relOriginal string) ([]byte, error) {
	full := filepath.Join(s.Root(), filepath.FromSlash(relOriginal))
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("uploads: read %q: %w", full, err)
	}

	return data, nil
}

// DeleteAll removes the original and any -s/-m/-l siblings.
// Best-effort: a missing variant is NOT an error (the worker may not have generated all three yet when the user deletes the post).
// The original being missing is also tolerated for the same reason.
// Returns the first non-IsNotExist error encountered, if any.
func (s *Storage) DeleteAll(relOriginal string) error {
	if relOriginal == "" {
		return nil
	}

	var firstErr error
	for _, suffix := range []string{"", "s", "m", "l"} {
		var target string
		if suffix == "" {
			target = filepath.Join(s.Root(), filepath.FromSlash(relOriginal))
		} else {
			target = s.variantPath(relOriginal, suffix)
		}

		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			if firstErr == nil {
				firstErr = fmt.Errorf("uploads: delete %q: %w", target, err)
			}
		}
	}

	return firstErr
}

func splitLastExt(p string) (base, ext string) {
	// Operate on the slash-separated path; the relPath we store is always slash-formatted regardless of OS.
	idx := strings.LastIndex(p, ".")
	if idx < 0 {
		return p, ""
	}

	return p[:idx], p[idx:]
}

// variantPath returns the absolute path for "<dir>/<base>-<suffix><ext>"
// given the original's relative path. Splits on the LAST dot so multi-dot names like "my.archive.jpg" stay correct.
func (s *Storage) variantPath(relOriginal, sizeSuffix string) string {
	base, ext := splitLastExt(relOriginal)
	return filepath.Join(s.Root(), filepath.FromSlash(base+"-"+sizeSuffix+ext))
}

// sanitizeBase strips the directory and extension off uploadedName, runs the remainder through slug.Generate,
// and falls back to a timestamped name if slug.Generate returns empty (e.g. a filename of only non-Latin/non-digit chars survives nothing).
func sanitizeBase(uploadedName string, today time.Time) string {
	name := filepath.Base(uploadedName)
	// Strip any extension (sanitize the stem only — we know the canonical ext from the image format).
	if dot := strings.LastIndex(name, "."); dot > 0 {
		name = name[:dot]
	}

	s := slug.Generate(name)
	if s == "" {
		s = "image-" + strconv.FormatInt(today.UTC().Unix(), 10)
	}

	return s
}

// candidateName returns base.ext for i=0, base-2.ext for i=1, base-3.ext for i=2, etc.
func candidateName(base string, i int, ext string) string {
	if i == 0 {
		return base + ext
	}

	return base + "-" + strconv.Itoa(i+1) + ext
}
