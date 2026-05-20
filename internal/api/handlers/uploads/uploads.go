// Package uploads serves files written by internal/uploads (post featured images and their variants) at /uploads/{path}.
// Public route — no auth.
// Cache-Control is set to immutable so browsers / CDNs can hold these indefinitely..
package uploads

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Handler returns an http.HandlerFunc serving the contents of root.
// The chi route pattern /uploads/* causes chi to set the URL path to "/uploads/2026/02/03/foo.jpg" -
// we strip the prefix and pass to http.ServeFile after a manual existence check (lets us return 404 rather than the default 403 for missing files).
func Handler(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/uploads/")
		if rel == "" || rel == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		// Defense in depth — chi already rejects "/uploads/..", but disallow any traversal element that slipped through (e.g. URL-encoded).
		if strings.Contains(rel, "..") {
			http.NotFound(w, r)
			return
		}

		full := filepath.Join(root, filepath.FromSlash(rel))
		// Re-anchor the joined path under root and confirm it didn't escape.
		// filepath.Join + the strings.Contains check above are belt-and-suspenders against path traversal.
		absRoot, _ := filepath.Abs(root)
		absFull, _ := filepath.Abs(full)
		if !strings.HasPrefix(absFull, absRoot+string(filepath.Separator)) && absFull != absRoot {
			http.NotFound(w, r)
			return
		}

		fi, err := os.Stat(full)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}

		if fi.IsDir() {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, full)
	}
}
