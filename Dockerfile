# syntax=docker/dockerfile:1

# ---- Frontend build stage ---------------------------------------------------
# The React app is served BY the Go binary (single container) via //go:embed,
# so we build it here and copy the dist output into the Go build stage before
# `go build` so the embed picks up the real bundle (the host web/dist holds only
# a committed placeholder, which .dockerignore excludes anyway).
FROM node:22-alpine AS web

WORKDIR /web

# Install deps on a cached layer keyed by the manifest. Prefer the reproducible
# `npm ci` when a lockfile is present; fall back to `npm install` for the very
# first build before package-lock.json has been committed.
COPY web/package.json web/package-lock.json* ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi

# Build the SPA → /web/dist
COPY web/ ./
RUN npm run build

# ---- Build stage ------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Cache module downloads on a separate layer from the source.
COPY go.mod go.sum ./
RUN go mod download

# Source (the .dockerignore keeps secrets/state/VCS out; api/ and the embedded
# migrations under cmd/migrate/migrations are included on purpose).
COPY . .

# Overlay the freshly-built SPA so //go:embed all:dist embeds the real bundle
# instead of the committed placeholder.
COPY --from=web /web/dist ./web/dist

# Pure-Go SQLite (modernc.org/sqlite) means CGO_ENABLED=0 yields a fully static
# binary. Build both the server and the migrate CLI so `migrate seed|status|down`
# can run inside the same image. -trimpath + -ldflags strips paths and debug
# info for a smaller, reproducible binary.
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags="-s -w" -o /out/server  ./cmd/server && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# ---- Runtime stage ----------------------------------------------------------
FROM alpine:3.20 AS runtime

# ca-certificates for any future outbound TLS; wget (busybox) backs the
# healthcheck. tzdata is optional and omitted to keep the image small.
RUN apk add --no-cache ca-certificates && \
    addgroup -S app && adduser -S -G app -h /app app

WORKDIR /app
COPY --from=build /out/server  /app/server
COPY --from=build /out/migrate /app/migrate

# Create the state dir BEFORE it becomes a volume mount target and hand it to
# the non-root user. Docker initializes a fresh NAMED volume with the image
# directory's contents+ownership, so the volume inherits `app` ownership and
# the server (running as `app`) can write the SQLite file + uploads.
RUN mkdir -p /data/uploads && chown -R app:app /data

USER app

# Defaults assume the compose-mounted /data volume; override via env as needed.
ENV PORT=8080 \
    DB_STRING=/data/vault.db \
    UPLOADS_DIR=/data/uploads \
    GO_ENV=prod

EXPOSE 8080

# GET / (the public Home route) returns 200 — cheap liveness probe.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${PORT}/" >/dev/null 2>&1 || exit 1

# Exec form → the server is PID 1 and receives SIGTERM directly, so the
# existing graceful-shutdown path (10s) fires on `docker stop`.
CMD ["/app/server"]
