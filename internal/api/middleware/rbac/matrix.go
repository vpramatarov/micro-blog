// Package rbac holds the role-based access control middleware: Bouncer (which
// enforces the centralized permission/scope matrix per route) and RequireRole
// (a simpler hard-role gate used for /admin/users etc.). Both write the same
// {"error":"forbidden"} envelope on denial — kept terse so callers can't inspect why they were denied.
package rbac

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ActionRule is one row in the centralized permission matrix.
//
// OwnerKind discriminates which typed repo Bouncer should query for the `own` scope.
// It's "" when the rule has no ownership concept (creates), and otherwise one of: "post", "shortlink".
type ActionRule struct {
	Permission    string
	ResourceParam string // chi URL param name (e.g. "id"); empty when no ownership check applies
	OwnerKind     string // "" / "post" / "shortlink"
}

// Matrix keys are "METHOD pattern" where pattern comes from chi's RoutePattern.
// Routes not in the matrix are not gated by the bouncer — register them under a
// non-/api prefix or add them here explicitly.
// Notes on what's NOT in this matrix:
//   - Public GET /posts, GET /posts/{id} — no auth required at all.
//   - GET /admin/post/{id} — any authenticated user; gate is just Authenticate.
//   - /admin/users/*, /admin/roles/*, /admin/permissions/* — gated by
//     RequireRole("Admin"), not this permission matrix.
var Matrix = map[string]ActionRule{
	"POST /admin/posts":           {Permission: "post:create"},
	"PUT /admin/posts/{id}":       {Permission: "post:edit", ResourceParam: "id", OwnerKind: "post"},
	"DELETE /admin/posts/{id}":    {Permission: "post:delete", ResourceParam: "id", OwnerKind: "post"},
	"POST /api/shortlinks":        {Permission: "shortlink:create"},
	"PUT /api/shortlinks/{id}":    {Permission: "shortlink:edit", ResourceParam: "id", OwnerKind: "shortlink"},
	"DELETE /api/shortlinks/{id}": {Permission: "shortlink:delete", ResourceParam: "id", OwnerKind: "shortlink"},
}

func routePattern(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return ""
	}

	return rctx.RoutePattern()
}
