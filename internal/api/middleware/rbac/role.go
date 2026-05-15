package rbac

import (
	"log/slog"
	"net/http"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	"github.com/vpramatarov/micro-blog/internal/auth"
)

// RequireRole denies any authenticated request whose claims.Role does not
// match `role`. Mount after Authenticate so claims are guaranteed in context.
func RequireRole(role string, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.FromContext(r.Context())
			if !ok {
				httpx.WriteForbidden(w)
				return
			}

			if claims.Role != role {
				log.Warn("role_denied",
					"user_id", claims.UserID,
					"role", claims.Role,
					"required", role,
					"method", r.Method,
					"path", r.URL.Path,
				)
				httpx.WriteForbidden(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
