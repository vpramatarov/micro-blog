package security

import "net/http"

// DefaultBodyLimit caps inbound JSON requests; 1 MiB is well above any legitimate payload this API accepts and bounds memory under abuse.
const DefaultBodyLimit int64 = 1 << 20

// LimitBody wraps r.Body with http.MaxBytesReader, so a Read past the limit returns *http.MaxBytesError.
// Handlers' existing json.Decode error path surfaces that as the standard 400 invalid_body envelope — no per-handler changes needed.
// Without this, a slow / huge client can tie up a goroutine until the chi.Timeout fires.
func LimitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}

			next.ServeHTTP(w, r)
		})
	}
}
