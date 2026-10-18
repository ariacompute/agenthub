# agenthub — A2A interconnection platform
#
# Stage 1 builds the Vue 3 console, stage 2 the Go binary, stage 3 is the
# runtime image that hosts both from a single process. PostgreSQL is the only
# state: the binary carries its migrations embedded, so no volume is needed.
#
# ---------------------------------------------------------------------------
# Build & run
# ---------------------------------------------------------------------------
# The build fetches npm packages (bun) and Go modules, so it needs outbound
# network. Two equivalent flows are supported:
#
#   1) Manual build, then compose up (reuses the pre-built image):
#        docker build \
#          --network=host \
#          --build-arg BUILD_PROXY=http://127.0.0.1:7897 \
#          -f Dockerfile -t agenthub .
#        docker compose up -d
#
#   2) One-shot compose build + run:
#        export BUILD_PROXY=http://127.0.0.1:7897   # optional; empty = direct
#        docker compose up -d --build
#
# `make docker-build` runs flow 1 with BUILD_PROXY picked up from $HTTP_PROXY.
#
# Build-time args (all optional, defaults baked into the Dockerfile):
#   * BUILD_PROXY  HTTP(S) proxy for bun and go during the build. Empty => a
#                 direct internet connection. Use `--network=host` so the
#                 container's 127.0.0.1 reaches a host-side proxy (e.g. Clash on
#                 127.0.0.1:7897); compose builds on the default bridge network,
#                 where that address does not exist — those builds take the
#                 no-proxy fallback below.
#   * GOPROXY      Go module proxy. Defaults to the goproxy.cn mirror, which is
#                 reachable without a proxy in most networks; override with
#                 `--build-arg GOPROXY=https://proxy.golang.org,direct`.
#   * VERSION / COMMIT / DISTANCE / DIRTY
#                 Build stamp reported by /healthz and shown in the console as
#                 "version@commit". `.git` is excluded by .dockerignore, so the
#                 values have to be passed in; `make docker-build` derives them.
# ---------------------------------------------------------------------------

# ---------- console ----------
FROM oven/bun:1-alpine AS web-build

# Re-declared per stage: each FROM starts a fresh scope.
ARG BUILD_PROXY=
ENV HTTP_PROXY=${BUILD_PROXY} \
    HTTPS_PROXY=${BUILD_PROXY} \
    http_proxy=${BUILD_PROXY} \
    https_proxy=${BUILD_PROXY}

WORKDIR /web
COPY web/package.json web/bun.lock ./
# NO_PROXY_FALLBACK: BUILD_PROXY usually points at 127.0.0.1:<port> on the host,
# which only resolves inside the container when the build uses --network=host.
# On the bridge network the first attempt fails, so retry with the proxy
# variables cleared — a build with direct internet then still succeeds.
RUN bun install --frozen-lockfile || \
    env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy bun install --frozen-lockfile

COPY web/ ./
# vite.config.js writes the bundle to ../backend/static, i.e. /backend/static.
RUN bun run build && test -f /backend/static/index.html

# ---------- server ----------
FROM golang:1.27-alpine AS build

ARG BUILD_PROXY=
ARG GOPROXY=https://goproxy.cn,direct

# .git is excluded by .dockerignore, so the build stamp has to be passed in.
ARG VERSION=dev
ARG COMMIT=unknown
ARG DISTANCE=0
ARG DIRTY=false

ENV GOTOOLCHAIN=local \
    CGO_ENABLED=0 \
    GOPROXY=${GOPROXY} \
    GOFLAGS=-mod=mod \
    HTTP_PROXY=${BUILD_PROXY} \
    HTTPS_PROXY=${BUILD_PROXY} \
    http_proxy=${BUILD_PROXY} \
    https_proxy=${BUILD_PROXY}

WORKDIR /src
COPY go.mod go.sum ./
# Same no-proxy fallback as above, then a loud failure with a hint instead of
# hanging on a dial timeout.
RUN go mod download || \
    env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy go mod download || \
    { echo "go mod download failed: set --build-arg BUILD_PROXY=<proxy> together with --network=host, or --build-arg GOPROXY=<mirror>"; exit 1; }

COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN mkdir -p /out && \
    go build -trimpath \
    -ldflags="-s -w -X agenthub/internal/config.Version=${VERSION} -X agenthub/internal/config.Commit=${COMMIT} -X agenthub/internal/config.Distance=${DISTANCE} -X agenthub/internal/config.Dirty=${DIRTY}" \
    -o /out/agenthub-server ./cmd/agenthub-server

# ---------- runtime ----------
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -g 10001 agenthub && \
    adduser -D -u 10001 -G agenthub agenthub && \
    mkdir -p /app/backend/static && \
    chown -R agenthub:agenthub /app

WORKDIR /app
COPY --from=build /out/agenthub-server /app/agenthub-server
COPY --from=web-build /backend/static /app/backend/static

# The console bundle is served by the Go process; every byte of state lives in
# PostgreSQL, so the container stays stateless. DATABASE_URL is mandatory:
# config.Validate refuses to start without it.
# dev is the safe default: `AGENTHUB_ENV=prod` additionally requires
# AGENTHUB_ADMIN_TOKEN (config.Validate refuses an open production deployment).
ENV AGENTHUB_HTTP_PORT=3500 \
    AGENTHUB_ENV=dev \
    AGENTHUB_STATIC_DIR=/app/backend/static

USER agenthub
EXPOSE 3500

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=5 \
    CMD wget -qO- http://127.0.0.1:3500/healthz >/dev/null || exit 1

ENTRYPOINT ["/app/agenthub-server"]
