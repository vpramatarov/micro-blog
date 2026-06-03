# Micro-Blog

A Go HTTP API for a **markdown micro-blog with a built-in URL shortener**, secured with dual-token JWT authentication and a scoped role-based access control (RBAC) model. SQLite is used for database. The full route surface is published as a hand-written OpenAPI 3.0.3 spec served alongside Swagger UI.


## Tech stack & dependencies

**Go 1.26** — module path `github.com/vpramatarov/micro-blog`.

## Quick start

```powershell
# 1. Install Go 1.26+ and clone the repo.
git clone https://github.com/vpramatarov/micro-blog
cd micro-blog

# 2. Pull dependencies.
go mod tidy

# 3. Optional: seed an admin user. ADMIN_SEED_PASSWORD is required — the
#    seed subcommand refuses to fall back to a known default. Migrations
#    don't need to be run by hand — the server auto-applies them at startup
#    (see `internal/migrate`).
$env:ADMIN_SEED_PASSWORD = "your-strong-password"
go run ./cmd/migrate seed

# 4. Run the server. Reads .env from cwd; auto-migrates to the latest schema
#    before binding the HTTP listener.
go run ./cmd/server
```

Then in a browser:
- `http://localhost:8080/docs` — Swagger UI, click *Authorize* and paste a bearer token to exercise endpoints.
- `http://localhost:8080/openapi.yaml` — raw spec.
- `http://localhost:8080/openapi.json` — JSON spec.

> The steps above run the **API only**. For the React frontend and the
> containerized workflows, see **[Setup & running](#setup--running)** below.

---

## Setup & running

The project is a Go API plus a React (Vite + TypeScript) single-page app. In
**production the Go binary serves everything** — the JSON API and the compiled
SPA, embedded into the binary via `//go:embed`. In **development** you can either
let Go serve a pre-built SPA, or run the Vite dev server for hot-module reload
(HMR). Both a local (no Docker) and a Docker workflow are supported.

### Prerequisites

| Tool | Needed for |
|---|---|
| **Go 1.26+** | Building / running the API locally (the no-Docker path). |
| **Node 22+ & npm** | Building or developing the React frontend locally. |
| **Docker Desktop (Compose v2)** | The Docker path — nothing else required; the image builds both Go and the frontend for you. |

### Configure `.env`

All config is environment-driven. Copy the template and set at least a JWT secret:

```powershell
copy .env.example .env
```

Then edit `.env`:
- `JWT_SECRET` — **required**, ≥ 32 bytes. Generate one with `openssl rand -base64 48`
  (or in PowerShell: `[Convert]::ToBase64String([byte[]](1..48 | % {Get-Random -Maximum 256}))`).
- For local non-HTTPS dev, set `COOKIE_SECURE=false` and `GO_ENV=dev` (so the
  refresh cookie is actually sent and migration logs are verbose).
- `ADMIN_SEED_PASSWORD` — set this if you intend to seed the admin user.

The full variable list is in **[Configuration](#configuration)**. In the Docker
workflows, container-specific values (`DB_STRING=/data/vault.db`,
`UPLOADS_DIR=/data/uploads`, `PORT`, `GO_ENV`, `COOKIE_SECURE`) are pinned by the
Compose files and override whatever is in `.env`; only `JWT_SECRET` (and
`ADMIN_SEED_PASSWORD` for seeding) need to come from `.env`.

### Run without Docker

```powershell
git clone https://github.com/vpramatarov/micro-blog
cd micro-blog
go mod tidy
```

**API only** (the SPA is served from whatever is in `web/dist`; a fresh checkout
ships a "frontend not built yet" placeholder there):

```powershell
go run ./cmd/server      # auto-migrates, then listens on :8080 (PORT)
```

**With the React frontend — two options:**

*Option 1 — Go serves the built SPA (single origin, no Node process at runtime):*
```powershell
cd web
npm install
npm run build            # outputs web/dist, which the server embeds via //go:embed
cd ..
go run ./cmd/server      # full app at http://localhost:8080
```
`go run`/`go build` embed `web/dist` **at compile time** — after changing the
frontend, re-run `npm run build` and restart the server. For an iterative
frontend loop use Option 2.

*Option 2 — Vite dev server with hot reload (two terminals):*
```powershell
# Terminal 1 — the API
go run ./cmd/server                 # http://localhost:8080

# Terminal 2 — the Vite dev server
cd web
npm install
npm run dev                          # http://localhost:5173  ← open this
```
Vite serves the SPA with HMR and proxies API calls (`/auth`, `/api`, `/admin`,
`/posts`, …) to the Go server on `:8080`, so the browser sees a single origin and
the refresh cookie works (with `COOKIE_SECURE=false`).

### Run with Docker

A multi-stage `Dockerfile` builds the frontend (Node stage) and a fully static
Go binary that embeds it (pure-Go SQLite, no CGO). Two Compose files:
`docker-compose.yml` (production baseline) and `docker-compose.override.yml`
(dev; auto-merged by `docker compose`).

**Development (hot reload) — recommended for local work:**
```powershell
copy .env.example .env               # set JWT_SECRET (+ ADMIN_SEED_PASSWORD)
docker compose up --build
```
This starts two services:
- **`api`** — the Go server with live reload (`air`) at **http://localhost:8080**.
- **`web`** — the Vite dev server with HMR at **http://localhost:5173** ← *open this*.

Edit anything under `web/` and the browser hot-updates; edit Go source and `air`
rebuilds the API. The `web` container proxies API calls to the `api` service over
the Compose network (`http://api:8080`).

**Production-like (single container, embedded SPA):**
```powershell
docker compose -f docker-compose.yml build     # Node builds web/dist, Go embeds it
docker compose -f docker-compose.yml up -d
# API + SPA served together at http://localhost:8080
```
The base stack sets `GO_ENV=prod` and `COOKIE_SECURE=true` — it expects a
TLS-terminating proxy in front. For plain-HTTP local testing, prefer the dev
stack (or set `COOKIE_SECURE=false`). Data (the SQLite file and uploaded images)
persists in the `appdata` named volume at `/data`.

### Seed data (admin + demo content)

The schema is applied automatically on server start; seeding is separate.

- **Admin user** (any environment) — reads `ADMIN_SEED_PASSWORD`:
  ```powershell
  # Local
  $env:ADMIN_SEED_PASSWORD = "your-strong-password"; go run ./cmd/migrate seed
  # Docker (dev stack)
  docker compose run --rm api go run ./cmd/migrate seed
  # Docker (prod stack — the migrate binary is baked into the image)
  docker compose -f docker-compose.yml run --rm api /app/migrate seed
  ```

- **Demo content** (users, categories, tags, 10–20 posts; **dev/test only** — it
  refuses to run under `GO_ENV=prod` without `-force`):
  ```powershell
  # Local
  go run ./cmd/migrate seed-demo -reset
  # Docker (dev stack — uses the bind-mounted source via `go run`)
  docker compose run --rm api go run ./cmd/migrate seed-demo -reset
  ```
  `-reset` wipes existing demo content (keeping the admin, RBAC rows, and the
  `Uncategorized` category) and reseeds a reproducible set; the command prints the
  demo logins (shared password defaults to `password123`).

### Where things live

| URL | What |
|---|---|
| `http://localhost:5173` | The React app with HMR (Docker dev stack, or local `npm run dev`). |
| `http://localhost:8080` | The API; also the full app when the SPA is built/embedded (local + Docker dev). |
| `http://localhost:8080` | The API + embedded SPA (Docker production-like stack). |
| `…/docs` | Swagger UI. `…/openapi.yaml` · `…/openapi.json` — the spec. |

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
| `UPLOADS_DIR` | `./uploads` | Directory where featured images + variants are written and served from. The Docker stacks point it at the `/data` volume (`/data/uploads`). |
| `GO_ENV` | `prod` | `dev` or `prod` — controls goose verbosity at server startup and is stamped on the startup log line. Default `prod` is fail-safe: production deploys can omit the var, while local `.env` sets `dev` so migration output is verbose. Anything other than `dev` / `prod` is rejected by `ValidateForServer`. |
| `ADMIN_SEED_PASSWORD` | _required_ | Read by `go run ./cmd/migrate seed`. The subcommand exits with an error if unset — no `changeme` fallback. |

---

## Project structure

The `internal/api/{handlers,middleware,repository}` trees are split by feature (or by mechanism, for middleware). No god-struct aggregator — each handler `Service` and each `Repo` is its own type, and the entrypoint wires only the deps each service actually needs.

```
.
├── api/
│   ├── embed.go                          # //go:embed openapi.yaml → api.Spec / api.SpecJSON / SpecJSONByRole
│   ├── filter.go                         # role-filtered spec variants
│   └── openapi.yaml                      # canonical OpenAPI 3.0.3 spec (source of truth)
├── cmd/
│   ├── embed.go                          # //go:embed migrate/migrations/*.sql
│   ├── migrate/                          # goose CLI + seed / seed-demo subcommands
│   │   ├── main.go
│   │   ├── seed_demo.go                  # `seed-demo` — demo fixtures via gofakeit (dev/test)
│   │   └── migrations/                   # 0000{1..10}*.sql
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
│   │   │   ├── docs/                     # /openapi.{yaml,json} + Swagger UI at /docs
│   │   │   ├── uploads/                  # GET /uploads/* — static featured-image serving
│   │   │   └── ui/                      # serves the embedded React SPA UI (index + NotFound fallback)
│   │   ├── middleware/
│   │   │   ├── auth/                     # Authenticate (Bearer → *auth.Claims in context)
│   │   │   ├── rbac/                     # Bouncer + RequireRole + RequireAnyRole + matrix
│   │   │   ├── security/                 # SecurityHeaders + LimitBody (+ DefaultBodyLimit)
│   │   │   └── observability/            # RequestLogger (slog JSON access log)
│   │   ├── repository/
│   │   │   ├── users/                    # User, UserUpdate, Repo, ErrUserNotFound/Duplicate
│   │   │   ├── posts/                    # Post, Repo, ErrPostNotFound, FindAvailableSlug (delegates to slug.Finder)
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
├── web/                                  # React + Vite + TypeScript SPA (embedded into the server)
│   ├── embed.go                          # //go:embed dist → served by the Go binary in prod
│   ├── src/                              # app shell: routing, auth context, typed API client, pages
│   ├── dist/                             # build output (placeholder committed; npm run build overwrites)
│   ├── vite.config.ts                    # dev proxy → API; HMR (polling under Docker)
│   ├── Dockerfile.dev                    # Vite dev-server image (compose `web` service)
│   └── package.json
├── Dockerfile                            # prod multi-stage: build SPA + static Go binary that embeds it
├── Dockerfile.dev                        # dev image: air live-reload for the API
├── docker-compose.yml                    # prod baseline (single container)
├── docker-compose.override.yml           # dev overrides: api (air) + web (Vite HMR)
├── .env.example                          # environment template
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
| Public | `GET /`, `GET /posts`, `GET /posts/{slug}`, `GET /p/{code}`, `GET /s/{code}`, `GET /categories`, `GET /categories/{slug}`, `GET /tags`, `GET /tags/{slug}` | none |
| Docs | `GET /openapi.{yaml,json}`, `GET /docs` | none |
| Auth | `POST /auth/{register,login,refresh,logout}` | refresh + logout need cookie |
| Profile | `GET /api/me`, `PUT /api/me` | any authenticated role |
| Shortlinks | `GET /api/shortlinks` (role-filtered list); `POST /api/shortlinks`, `PUT /api/shortlinks/{id}`, `DELETE /api/shortlinks/{id}` (bouncer-gated) | bearer |
| Posts | `GET /admin/posts`, `GET /admin/categories/{slug}`, `GET /admin/tags/{slug}` (role-filtered lists); `POST /admin/posts`, `PUT /admin/posts/{id}`, `DELETE /admin/posts/{id}` (bouncer-gated) | bearer |
| Categories | `POST /admin/categories`, `PUT /admin/categories/{id}`, `DELETE /admin/categories/{id}` | bearer, Admin or Editor |
| Tags | `POST /admin/tags`, `PUT /admin/tags/{id}`, `DELETE /admin/tags/{id}` | bearer, Admin or Editor |
| Admin posts | `GET /admin/post/{id}` (numeric-id read) | bearer, Admin only |
| Admin users | `GET /admin/users`, `GET /admin/users/{id}`, `POST /admin/users`, `PUT /admin/users/{id}`, `DELETE /admin/users/{id}` | bearer, Admin only |

---


## Using the OpenAPI spec

### Reading it
- **Browser**: hit `GET /docs` (Swagger UI) or `GET /openapi.yaml`.
- **CLI consumers**: `curl http://localhost:8080/openapi.json | jq` returns the JSON-converted spec (computed once at process init).

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

