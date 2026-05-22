package auth

import (
	"log/slog"
	"net/http"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	"github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
	"github.com/vpramatarov/micro-blog/internal/auth"
)

// Authenticate parses the Bearer token, validates it via the issuer.
// On failure it writes a 401 JSON envelope and short-circuits — downstream handlers can rely on auth.FromContext returning a valid claims pointer.
// tokens repo may be nil in tests that don't care about revocation.
func Authenticate(issuer *auth.Issuer, tokensRepo *tokens.Repo, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := httpx.BearerToken(r)
			if tok == "" {
				httpx.WriteUnauthorized(w, "missing_token")
				return
			}

			claims, err := issuer.Parse(tok)
			if err != nil {
				log.Info("auth_failed", "err", err.Error(), "path", r.URL.Path)
				httpx.WriteUnauthorized(w, "invalid_token")
				return
			}

			if tokensRepo != nil && claims.ID != "" {
				revoked, err := tokensRepo.IsJTIRevoked(r.Context(), claims.ID)
				if err != nil {
					log.Error("revocation check failed", "err", err.Error(), "path", r.URL.Path)
					httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not validate token")
					return
				}

				if revoked {
					httpx.WriteUnauthorized(w, "invalid_token")
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(auth.WithClaims(r.Context(), claims)))
		})
	}
}
