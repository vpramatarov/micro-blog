package rbac

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	"github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	rbacRepo "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	"github.com/vpramatarov/micro-blog/internal/api/repository/shortlinks"
	"github.com/vpramatarov/micro-blog/internal/auth"
)

// Bouncer enforces the centralized permission/scope matrix. Mount it after
// Authenticate so claims are guaranteed in context.
//
// The three repo arguments cover the queries Bouncer makes directly:
//   - scopeRepo.GetRolePermissionScope — look up the role's scope for the
//     action (all/own/none/"").
//   - postsRepo.GetOwnerID / shortLinksRepo.GetOwnerID —
//     resolve the resource's owner when scope='own'.
func Bouncer(
	scopeRepo *rbacRepo.Repo,
	postsRepo *posts.Repo,
	shortLinksRepo *shortlinks.Repo,
	log *slog.Logger,
) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}

	// ownerLookup dispatches on rule.OwnerKind to the right repo. Captured by closure so Matrix can stay a static var (no repos inside it).
	ownerLookup := func(ctx context.Context, kind string, id int64) (int64, error) {
		switch kind {
		case "post":
			return postsRepo.GetOwnerID(ctx, id)
		case "shortlink":
			return shortLinksRepo.GetOwnerID(ctx, id)
		default:
			return 0, errors.New("rbac: no owner lookup configured for kind " + kind)
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.FromContext(r.Context())
			if !ok {
				httpx.WriteForbidden(w)
				return
			}

			pattern := routePattern(r)
			rule, found := Matrix[r.Method+" "+pattern]
			if !found {
				next.ServeHTTP(w, r)
				return
			}

			scope, err := scopeRepo.GetRolePermissionScope(r.Context(), claims.RoleID, rule.Permission)
			if err != nil {
				log.Error("rbac_lookup_failed", "err", err, "user_id", claims.UserID, "permission", rule.Permission)
				httpx.WriteForbidden(w)
				return
			}

			if scope == "" || scope == "none" {
				logDeny(log, claims, rule, scope, r, "no permission")
				httpx.WriteForbidden(w)
				return
			}

			if scope == "all" {
				next.ServeHTTP(w, r)
				return
			}

			// scope == "own"
			if rule.OwnerKind == "" || rule.ResourceParam == "" {
				logDeny(log, claims, rule, scope, r, "own scope but no lookup configured")
				httpx.WriteForbidden(w)
				return
			}

			idStr := chi.URLParam(r, rule.ResourceParam)
			resourceID, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				logDeny(log, claims, rule, scope, r, "invalid resource id")
				httpx.WriteForbidden(w)
				return
			}

			ownerID, err := ownerLookup(r.Context(), rule.OwnerKind, resourceID)
			if err != nil {
				// Resource missing or lookup failed — same response either way to avoid leaking existence.
				logDeny(log, claims, rule, scope, r, "owner lookup failed: "+errMsg(err))
				httpx.WriteForbidden(w)
				return
			}

			if ownerID != claims.UserID {
				logDeny(log, claims, rule, scope, r, "owner mismatch")
				httpx.WriteForbidden(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func logDeny(log *slog.Logger, c *auth.Claims, rule ActionRule, scope string, r *http.Request, reason string) {
	log.Warn("rbac_denied",
		"user_id", c.UserID,
		"role", c.Role,
		"permission", rule.Permission,
		"scope", scope,
		"method", r.Method,
		"pattern", routePattern(r),
		"reason", reason,
	)
}

func errMsg(err error) string {
	if errors.Is(err, posts.ErrPostNotFound) ||
		errors.Is(err, shortlinks.ErrShortLinkNotFound) {
		return "not found"
	}

	return err.Error()
}
