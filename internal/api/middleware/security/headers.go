// Package security holds HTTP-layer hardening middleware: response-header
// defaults and request-body size limits. These run on every route — handlers
// don't opt in.
package security

import "net/http"

// The Strict-Transport-Security value emitted when HSTS is enabled.
const HSTSHeaderValue string = "max-age=31536000; includeSubDomains"

type Options struct {
	EnableHSTS bool
}

// SecurityHeaders applies a conservative default header set to every response:
//
//   - X-Content-Type-Options: nosniff   — refuse MIME-sniffed scripts
//   - X-Frame-Options: DENY             — refuse to be framed
//   - Referrer-Policy: no-referrer      — don't leak URLs to other origins
//   - Content-Security-Policy: locked-down JSON-API default
//
// Routes that need a different CSP (notably /docs, which loads Swagger UI
// assets from a CDN) should overwrite `Content-Security-Policy` on the
// response themselves — header set on a ResponseWriter replaces, not appends."
func SecurityHeaders(opts Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			headers := w.Header()
			headers.Set("X-Content-Type-Options", "nosniff")
			headers.Set("X-Frame-Options", "DENY")
			headers.Set("Referrer-Policy", "no-referrer")
			headers.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			if opts.EnableHSTS {
				headers.Set("Strict-Transport-Security", HSTSHeaderValue)
			}

			next.ServeHTTP(w, r)
		})
	}
}
