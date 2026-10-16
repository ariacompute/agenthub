# agenthub — A2A interconnection platform
#
# Stage 1 builds the Vue 3 console, stage 2 the Go binary, stage 3 is the
# runtime image that hosts both from a single process. PostgreSQL is the only
# state: the binary carries its migrations embedded, so no volume is needed.

# ---------- console ----------
FROM oven/bun:1-alpine AS web-build

WORKDIR /web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile

COPY web/ ./
# vite.config.js writes the bundle to ../backend/static, i.e. /backend/static.
RUN bun run build && test -f /backend/static/index.html

# ---------- server ----------
FROM golang:1.27-alpine AS build

# .git is excluded by .dockerignore, so the build stamp has to be passed in:
#   docker build --build-arg VERSION=$(git describe --tags --abbrev=0 --always) \
#                --build-arg COMMIT=$(git rev-parse --short=7 HEAD)
# `make docker-build` does this; compose forwards $VERSION / $COMMIT the same way.
ARG VERSION=dev
ARG COMMIT=unknown
ARG DISTANCE=0
ARG DIRTY=false

ENV GOTOOLCHAIN=local \
    CGO_ENABLED=0

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

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
