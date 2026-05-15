package auth

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	"github.com/vpramatarov/micro-blog/internal/auth"
)

// Authenticate parses the Bearer token, validates it via the issuer, and
// injects the resulting claims into the request context. On failure it writes
// a 401 JSON envelope and short-circuits — downstream handlers can rely on
// coreauth.FromContext returning a valid claims pointer.
func Authenticate(issuer *auth.Issuer, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := bearerToken(r)
			if tok == "" {
				writeUnauthorized(w, "missing_token")
				return
			}

			claims, err := issuer.Parse(tok)
			if err != nil {
				log.Info("auth_failed", "err", err.Error(), "path", r.URL.Path)
				writeUnauthorized(w, "invalid_token")
				return
			}

			next.ServeHTTP(w, r.WithContext(auth.WithClaims(r.Context(), claims)))
		})
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}

	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}

	return strings.TrimSpace(h[len(prefix):])
}

// unauthorizedMessages maps the structured `error` code to the human-readable
// `message` that the rest of the API emits via httpx.WriteError. Kept inline
// so the middleware doesn't import the handlers tier.
var unauthorizedMessages = map[string]string{
	"missing_token": "authentication required",
	"invalid_token": "token is invalid or expired",
}

func writeUnauthorized(w http.ResponseWriter, code string) {
	httpx.WriteError(w, http.StatusUnauthorized, code, unauthorizedMessages[code])
}
