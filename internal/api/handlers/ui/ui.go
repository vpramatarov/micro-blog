// Package ui serves the embedded React single-page app. It is wired as chi's
// NotFound handler (so it adds no registered routes — the openapi drift test,
// which walks registered routes only, is unaffected) plus the "/" index route.
//
// Behaviour for an unmatched request:
//   - a path under an API namespace (/api, /auth, /admin, …) → JSON 404, so the
//     API keeps its machine-readable error envelope instead of leaking HTML;
//   - an existing static asset in the build (e.g. /assets/app-<hash>.js) →
//     served from the embedded FS with a long immutable cache;
//   - anything else → index.html, so client-side routing (e.g. /dashboard) works
//     on a hard refresh. The index response carries a relaxed CSP (the global
//     default-src 'none' would block the bundle) scoped to this document only.
package ui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
)

// indexCSP loosens the strict global policy (security.SecurityHeaders sets default-src 'none') just enough for a Vite bundle: same-origin scripts,
// same-origin styles plus inline (Vite injects a small inline style), data: images, and same-origin XHR/fetch to the API. No wildcard, no unsafe-eval.
const indexCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'"

// apiPrefixes are the path namespaces owned by the JSON API. An unmatched request under any of these gets a JSON 404 rather than the SPA shell.
var apiPrefixes = []string{
	"/api", "/auth", "/admin", "/uploads",
	"/openapi", "/docs", "/s", "/p",
	"/posts", "/categories", "/tags",
}

func isAPIPath(p string) bool {
	for _, prefix := range apiPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}

	return false
}

// Handler is the chi NotFound handler: static-asset passthrough with an index.html SPA fallback.
func Handler(dist fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(dist))
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "not found")
			return
		}

		if isAPIPath(r.URL.Path) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "not found")
			return
		}

		// Serve a real build artifact if the path points at one.
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && fs.ValidPath(name) {
			if f, err := dist.Open(name); err == nil {
				info, statErr := f.Stat()
				_ = f.Close()
				if statErr == nil && !info.IsDir() {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}

		serveIndex(w, r, dist)
	}
}

// Index serves the SPA entrypoint for the "/" route (it is a registered chi route, so it can't go through the NotFound handler).
func Index(dist fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveIndex(w, r, dist)
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "frontend not available")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", indexCSP)
	// The HTML shell must not be cached hard — a redeploy ships a new index.html referencing freshly-hashed assets.
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}

	_, _ = w.Write(b)
}
