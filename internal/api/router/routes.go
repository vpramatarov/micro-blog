package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	"github.com/vpramatarov/micro-blog/internal/api/handlers/categories"
	"github.com/vpramatarov/micro-blog/internal/api/handlers/docs"
	"github.com/vpramatarov/micro-blog/internal/api/handlers/posts"
	"github.com/vpramatarov/micro-blog/internal/api/handlers/shortlinks"
	"github.com/vpramatarov/micro-blog/internal/api/handlers/tags"
	uploadsh "github.com/vpramatarov/micro-blog/internal/api/handlers/uploads"
	"github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	"github.com/vpramatarov/micro-blog/internal/api/httpx"
)

// Services bundles the per-feature handler services the router mounts. Every
// field is a concrete pointer — the router calls method names directly, so
// nil is only acceptable on services whose routes are not exercised by the
// caller (tests that, e.g., only hit /docs may pass nil for the others).
type Services struct {
	Auth        *auth.Service
	Users       *users.Service
	ShortLinks  *shortlinks.Service
	Posts       *posts.Service
	Categories  *categories.Service
	Tags        *tags.Service
	Docs        *docs.Service
	UploadsRoot string
}

// Middlewares bundles the route-scoped middleware the router needs to mount on
// specific groups, plus the global chain that runs on every request. Each of
// Auth / Bouncer / RequireAdmin may be nil — the router skips a nil entry,
// which lets tests opt out of middleware they don't care about.
type Middlewares struct {
	// Auth gates /api/* and /admin/*. Parses the Bearer token and injects claims into the request context.
	Auth func(http.Handler) http.Handler

	// Bouncer enforces the centralized permission/scope matrix. Mounted on subgroups within /api/* and /admin/* that need matrix-gated writes.
	Bouncer func(http.Handler) http.Handler

	// RequireAdmin is the simpler hard-role gate used by the Admin-only subtree (/admin/post/{id}, /admin/users/*).
	RequireAdmin func(http.Handler) http.Handler

	// RequireEditorOrAdmin permits the Admin + Editor roles. Used by the /admin/categories and /admin/tags write groups —
	// they have no ownership concept, so the Bouncer matrix is not the right gate.
	RequireEditorOrAdmin func(http.Handler) http.Handler

	// Global runs on every request. Mounted in the order given via chi.
	// Order matters (e.g. RequestID before RequestLogger so the log line can correlate by id).
	Global []func(http.Handler) http.Handler
}

// Route groups:
//   - /                                 public — Home
//   - GET /posts                        public — list posts (every response item carries a hashid `code` AND a `slug`)
//   - GET /posts/{slug}                 public — read a post by its auto-generated slug
//   - GET /p/{code}                     public — read a post by its sqids hashid (was /posts/{code} pre-categories)
//   - GET /s/{code}                     public — URL-shortener resolution; 302 to same-origin targets, HTML template for exernal hosts
//   - GET /categories,
//     GET /tags                         public — read-only taxonomy listings
//   - GET /uploads/*                    public — static file serving for post featured images and variants
//   - GET /openapi.yaml,
//     GET /openapi.json,
//     GET /docs                         public — OpenAPI spec + Swagger UI
//   - /auth/*                           public — register / login / refresh / logout
//   - /api/*                            authenticated
//   - GET  /api/me, PUT /api/me                 self-service profile (no role gate, no bouncer)
//   - GET  /api/shortlinks                      handler-filtered list (Admin: all; others: own)
//   - POST   /api/shortlinks,
//     PUT    /api/shortlinks/{id},
//     DELETE /api/shortlinks/{id}               bouncer-gated by shortlink:create/edit/delete
//   - /admin/*                          authenticated; sub-policies below
//   - GET /admin/posts              any authenticated user; Authors see only own posts
//   - POST /admin/posts,
//     PUT /admin/posts/{id},
//     DELETE /admin/posts/{id}      bouncer-gated: Admin/Editor=all, Author=own, Subscriber=denied
//   - POST   /admin/categories,
//     PUT    /admin/categories/{id},
//     DELETE /admin/categories/{id},
//     POST   /admin/tags,
//     PUT    /admin/tags/{id},
//     DELETE /admin/tags/{id}       Admin + Editor only (RequireEditorOrAdmin)
//   - GET /admin/post/{id}          Admin role only — numeric-id read
//   - GET    /admin/users,
//     GET    /admin/users/{id},
//     POST   /admin/users,
//     PUT    /admin/users/{id},
//     DELETE /admin/users/{id}      Admin role only — user CRUD
func New(srvc Services, mw Middlewares) *chi.Mux {
	r := chi.NewRouter()

	for _, middleware := range mw.Global {
		r.Use(middleware)
	}

	// Routes
	r.Get("/", home)

	// Public post reads — no auth. Read-by-id is intentionally absent here;
	// only the hashid-encoded `{code}` route exists publicly.
	r.Get("/posts", srvc.Posts.List)
	r.Get("/p/{code}", srvc.Posts.GetByCode)
	r.Get("/posts/{slug}", srvc.Posts.GetBySlug)
	// Public URL-shortener resolution. Decodes the hashid back to a row and 302-redirects to the original URL.
	r.Get("/s/{code}", srvc.ShortLinks.Resolve)

	// Public taxonomy reads.
	r.Get("/categories", srvc.Categories.List)
	r.Get("/tags", srvc.Tags.List)

	// Public static asset serving for uploaded post images. The handler rejects path traversal and 404s for missing files.
	// Skipped entirely when UploadsRoot is empty (tests that don't exercise uploads).
	if srvc.UploadsRoot != "" {
		r.Get("/uploads/*", uploadsh.Handler(srvc.UploadsRoot))
	}

	// API documentation
	// /openapi.yaml is the canonical spec;
	// /openapi.json is the same content round-tripped through JSON; /docs renders Swagger UI pointing at /openapi.json.
	r.Get("/openapi.yaml", srvc.Docs.ServeOpenAPIYAML)
	r.Get("/openapi.json", srvc.Docs.ServeOpenAPIJSON)
	r.Get("/docs", srvc.Docs.ServeDocs)

	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", srvc.Auth.Register)
		r.Post("/login", srvc.Auth.Login)
		r.Post("/refresh", srvc.Auth.Refresh)
		r.Post("/logout", srvc.Auth.Logout)
	})

	r.Route("/api", func(r chi.Router) {
		if mw.Auth != nil {
			r.Use(mw.Auth)
		}

		// Self-service profile — any authenticated user acts on their own row.
		// Not bouncer-gated; the caller's id comes from the JWT, never from a URL param, so there's no scope check to perform.
		r.Get("/me", srvc.Users.GetMe)
		r.Put("/me", srvc.Users.UpdateMe)

		// Short-link list — handler-filtered (Admin sees all, everyone else  sees only their own).
		// Lives outside the bouncer subgroup because the filter is role-based at the handler, not matrix-gated.
		r.Get("/shortlinks", srvc.ShortLinks.List)

		// Permission/scope-gated routes go inside this group so unrelated endpoints (like /me, /shortlinks list) don't pay for a matrix lookup on every request.
		r.Group(func(r chi.Router) {
			if mw.Bouncer != nil {
				r.Use(mw.Bouncer)
			}

			r.Post("/shortlinks", srvc.ShortLinks.Create)
			r.Put("/shortlinks/{id}", srvc.ShortLinks.Update)
			r.Delete("/shortlinks/{id}", srvc.ShortLinks.Delete)
		})
	})

	r.Route("/admin", func(r chi.Router) {
		if mw.Auth != nil {
			r.Use(mw.Auth)
		}

		// Authenticated list available to every role. ListAdmin filters the result set by role (Authors see only their own posts).
		r.Get("/posts", srvc.Posts.ListAdmin)

		// Post writes — bouncer enforces post:create / post:edit / post:delete
		// against the role's scope. Authors can only act on their own posts;
		// Admin/Editor act on all; Subscriber is denied.
		r.Group(func(r chi.Router) {
			if mw.Bouncer != nil {
				r.Use(mw.Bouncer)
			}

			r.Post("/posts", srvc.Posts.Create)
			r.Put("/posts/{id}", srvc.Posts.Update)
			r.Delete("/posts/{id}", srvc.Posts.Delete)
		})

		// Categories / tags writes — Admin + Editor only. No ownership, so the Bouncer matrix is not the right gate; a flat role list is.
		r.Group(func(r chi.Router) {
			if mw.RequireEditorOrAdmin != nil {
				r.Use(mw.RequireEditorOrAdmin)
			}

			// categories
			r.Post("/categories", srvc.Categories.Create)
			r.Put("/categories/{id}", srvc.Categories.Update)
			r.Delete("/categories/{id}", srvc.Categories.Delete)
			// tags
			r.Post("/tags", srvc.Tags.Create)
			r.Put("/tags/{id}", srvc.Tags.Update)
			r.Delete("/tags/{id}", srvc.Tags.Delete)
		})

		// Admin-only subtree — by-id post read and user CRUD. Role and permission management will mount here too.
		r.Group(func(r chi.Router) {
			if mw.RequireAdmin != nil {
				r.Use(mw.RequireAdmin)
			}

			r.Get("/post/{id}", srvc.Posts.GetById)
			r.Get("/users", srvc.Users.List)
			r.Get("/users/{id}", srvc.Users.GetUser)
			r.Post("/users", srvc.Users.Create)
			r.Put("/users/{id}", srvc.Users.Update)
			r.Delete("/users/{id}", srvc.Users.Delete)
		})
	})

	return r
}

// home returns an empty JSON object — historically used by uptime checkers to
// verify the server is reachable. Bodyless 200s confuse some tooling, so we keep returning `{}`.
func home(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, struct{}{})
}
