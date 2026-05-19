# Micro-Blog

A Go HTTP API for a **markdown micro-blog with a built-in URL shortener**, secured with dual-token JWT authentication and a scoped role-based access control (RBAC) model. SQLite is used for database. The full route surface is published as a hand-written OpenAPI 3.0.3 spec served alongside Swagger UI.


## Tech stack & dependencies

**Go 1.26** — module path `github.com/vpramatarov/micro-blog`.

## Quick start

```powershell
# Install Go 1.26+ and clone the repo.
git clone https://github.com/vpramatarov/micro-blog
cd micro-blog

# Pull dependencies.
go mod tidy

# Apply migrations against vault.db (path from .env).
go run ./cmd/migrate up

# Optional: seed an admin user. ADMIN_SEED_PASSWORD is required — the
#    seed subcommand refuses to fall back to a known default.
$env:ADMIN_SEED_PASSWORD = "your-strong-password"
go run ./cmd/migrate seed

# 5. Run the server. Reads .env from cwd.
go run ./cmd/server
```

Then in a browser:
- `http://localhost:8090/docs` — Swagger UI, click *Authorize* and paste a bearer token to exercise endpoints.
- `http://localhost:8090/openapi.yaml` — raw spec.
- `http://localhost:8090/openapi.json` — JSON spec.

---

## Configuration

All config is environment-driven; `.env` and `.env.test` are loaded automatically (`internal/config/config.go` walks up to four parent directories to find them).

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8080` | TCP port the server listens on. |
| `DB_STRING` | `""` | SQLite file path (e.g. `vault.db`). Empty surfaces a misleading error on first query — set it. |
| `JWT_SECRET` | `""` | HMAC key for access tokens. **The API server refuses to start unless this is set to ≥ 32 bytes** (HS256 needs that much entropy). Generate one with e.g. `openssl rand -base64 48`. The migrate CLI does not enforce this — it only needs `DB_STRING`. |
| `JWT_ACCESS_TTL` | `15m` | `time.ParseDuration` syntax. |
| `JWT_REFRESH_TTL` | `168h` (7d) | `time.ParseDuration` syntax. |
| `JWT_ISSUER` | `micro-blog` | Written into the `iss` claim on every access token and verified on parse. Different envs should use different values to prevent cross-env token replay. |
| `JWT_AUDIENCE` | `micro-blog-api` | Written into the `aud` claim and verified on parse. |
| `COOKIE_SECURE` | `true` | Set to `false` for local non-HTTPS dev so the refresh cookie is actually sent. |
| `ADMIN_SEED_PASSWORD` | _required_ | Read by `go run ./cmd/migrate seed`. The subcommand exits with an error if unset — no `changeme` fallback. |

---

## Project structure

```
.
├── api/
│   ├── embed.go                          # //go:embed openapi.yaml → api.Spec / api.SpecJSON / SpecJSONByRole
│   ├── filter.go                         # role-filtered spec variants
│   └── openapi.yaml                      # canonical OpenAPI 3.0.3 spec (source of truth)
├── cmd/
│   ├── embed.go                          # //go:embed migrate/migrations/*.sql
│   ├── migrate/                          # goose CLI + seed subcommand
│   │   ├── main.go
│   │   └── migrations/                   # 0000{1..5}*.sql
│   └── server/main.go                    # entrypoint: wires repos, services, middleware, router
├── internal/
│   ├── api/
│   │   ├── httpx/                        # shared HTTP helpers: WriteJSON, WriteError,
│   │   │                                 # WriteValidationError, Page[T], ParsePagination
│   │   ├── handlers/
│   │   │   ├── auth/                     # Register, Login, Refresh, Logout (+ refresh cookie)
│   │   │   ├── users/                    # admin user CRUD + /api/me self-service (shared profile.go)
│   │   │   ├── posts/                    # public reads (incl. /posts/{slug}, /p/{code}) + /admin/posts CRUD
│   │   │   ├── shortlinks/               # /api/shortlinks CRUD + public /s/{code} resolve
│   │   │   ├── categories/               # public GET /categories + admin/editor CRUD
│   │   │   ├── tags/                     # public GET /tags + admin/editor CRUD
│   │   │   └── docs/                     # /openapi.{yaml,json} + Swagger UI at /docs
│   │   ├── middleware/
│   │   │   ├── auth/                     # Authenticate (Bearer → *auth.Claims in context)
│   │   │   ├── rbac/                     # Bouncer + RequireRole + RequireAnyRole + matrix
│   │   │   ├── security/                 # SecurityHeaders + LimitBody (+ DefaultBodyLimit)
│   │   │   └── observability/            # RequestLogger (slog JSON access log)
│   │   ├── repository/
│   │   │   ├── users/                    # User, UserUpdate, Repo, ErrUserNotFound/Duplicate
│   │   │   ├── posts/                    # Post, Repo, ErrPostNotFound/ErrPostDuplicateSlug, FindAvailableSlug
│   │   │   ├── shortlinks/               # ShortLink, Repo, ErrShortLinkNotFound
│   │   │   ├── tokens/                   # refresh-token Repo (with lazy purge), ErrRefreshTokenNotFound
│   │   │   ├── rbac/                     # RoleExists, GetRolePermissionScope
│   │   │   ├── categories/               # Category, Repo, ErrCategoryNotFound/Duplicate/InUse
│   │   │   └── tags/                     # Tag, Repo, post_tags M:N helpers
│   │   └── router/                       # chi route tree. `New(router.Services, router.Middlewares)`
│   │                                     # takes two typed bundles; nil middleware fields are skipped.
│   │                                     # Home is inlined here.
│   ├── auth/                             # password (bcrypt), jwt (issuer/parse + refresh-token helpers)
│   ├── config/                           # env loading
│   ├── markdown/                         # goldmark wrapper
│   ├── shortcode/                        # sqids wrapper
│   ├── slug/                             # title → slug; Bulgarian transliteration per the 2009 law
│   ├── testutil/                         # EnsureTestSchema, SetupTestDB, dbsmoke_test.go
│   └── validation/                       # per-field validators + Errors accumulator (no external deps)
├── CLAUDE.md                             # development conventions / non-obvious gotchas
├── PRD.md                                # product requirements
├── README.md                             # this file
└── go.mod
```

---

## API surface (high-level)

The full route map with request/response shapes is in `api/openapi.yaml` — use `GET /docs`.

| Group | Routes | Auth |
|---|---|---|
| Public | `GET /`, `GET /posts`, `GET /posts/{slug}`, `GET /p/{code}`, `GET /s/{code}`, `GET /categories`, `GET /tags` | none |
| Docs | `GET /openapi.{yaml,json}`, `GET /docs` | none |
| Auth | `POST /auth/{register,login,refresh,logout}` | refresh + logout need cookie |
| Profile | `GET /api/me`, `PUT /api/me` | any authenticated role |
| Shortlinks | `GET /api/shortlinks` (role-filtered list); `POST /api/shortlinks`, `PUT /api/shortlinks/{id}`, `DELETE /api/shortlinks/{id}` (bouncer-gated) | bearer |
| Posts | `GET /admin/posts` (role-filtered list); `POST /admin/posts`, `PUT /admin/posts/{id}`, `DELETE /admin/posts/{id}` (bouncer-gated) | bearer |
| Categories | `POST /admin/categories`, `PUT /admin/categories/{id}`, `DELETE /admin/categories/{id}` | bearer, Admin or Editor |
| Tags | `POST /admin/tags`, `PUT /admin/tags/{id}`, `DELETE /admin/tags/{id}` | bearer, Admin or Editor |
| Admin posts | `GET /admin/post/{id}` (numeric-id read) | bearer, Admin only |
| Admin users | `GET /admin/users`, `GET /admin/users/{id}`, `POST /admin/users`, `PUT /admin/users/{id}`, `DELETE /admin/users/{id}` | bearer, Admin only |

---


## Using the OpenAPI spec

### Reading it
- **Browser**: hit `GET /docs` (Swagger UI) or `GET /openapi.yaml`.
- **CLI consumers**: `curl http://localhost:8090/openapi.json | jq` returns the JSON-converted spec (computed once at process init).

### Using endpoints from the docs page
1. Open `/docs`.
2. Click *Authorize* → paste a bearer token obtained from `POST /auth/login`.
3. Expand any operation, click *Try it out*, fill in the body / params, *Execute*. The token is reused across operations (`persistAuthorization: true`).

### Using URL shortener
- `POST /api/shortlinks` saves a long URL; the row's auto-incrementing id is encoded with **sqids** into a short opaque code (`X7bL9q`-style).
- `GET /s/{code}` resolves anonymously: the code is decoded back to the id and the response 302-redirects to the stored URL. No separate UPDATE query — the code is derived from the id, not stored.
- `GET /api/shortlinks` is role-filtered (Admin sees all; everyone else sees own).
- PUT/DELETE are bouncer-gated on `shortlink:edit` / `shortlink:delete` (Editor/Author scoped to own).

### Adding a new endpoint — workflow
1. Add the handler + repository method (or wire the new route in `routes.go`).
2. Add the matching operation under `paths:` in `api/openapi.yaml`.
3. Run the test suite — the drift test `TestOpenAPISpecCoversEveryRoute` fails loud if the spec entry is missing.

---

## Running tests

The test suite is integration-heavy: most tests stand up a real router, the real auth chain, and a shared SQLite test database.

```powershell
# All tests — must serialize with -p 1 because every test binary opens the
# same vault_test.db.
go test -p 1 ./...

# A single package (no -p needed — only one binary)
go test ./internal/api/handlers/posts
go test ./internal/api/repository/users

# A single test by name
go test ./internal/api/handlers/posts -run TestCreatePostAsAuthor

# With verbose output
go test -p 1 -v ./...
```

## Common commands

```powershell
go run ./cmd/server                     # run server (loads .env)
go run ./cmd/migrate up                 # apply migrations
go run ./cmd/migrate down               # roll back the latest
go run ./cmd/migrate status             # which migrations applied
go run ./cmd/migrate seed               # insert admin user (uses ADMIN_SEED_PASSWORD)
go test -p 1 ./...                      # all tests
go mod tidy                             # after editing go.mod
```

---

