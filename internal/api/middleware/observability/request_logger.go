// Package observability holds middleware that exists to make the running
// server visible to operators — currently the structured access logger.
package observability

import (
	"log/slog"
	"net/http"
	"time"

	chiMW "github.com/go-chi/chi/v5/middleware"
)

// RequestLogger emits one structured slog line per HTTP request, replacing
// chi.middleware.Logger's text output so access logs share the same JSON
// format as the rest of the application.
//
// Fields: method, path, status, bytes, duration_ms, remote_addr, request_id
// (when chi.RequestID is in the chain). The line uses Info level regardless
// of status — error handlers already emit their own slog records at
// Error/Warn, so we don't duplicate those.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chiMW.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			defer func() {
				log.Info("http_request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"duration_ms", time.Since(start).Milliseconds(),
					"remote_addr", r.RemoteAddr,
					"request_id", chiMW.GetReqID(r.Context()),
				)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}
