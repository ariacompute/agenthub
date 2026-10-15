# agenthub — A2A interconnection platform
#
# Stage 1 builds the Vue 3 console, stage 2 the Go binary (zero third-party
# dependencies, so there is no go.sum to copy), stage 3 is the runtime image
# that hosts both from a single process.

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

ENV GOTOOLCHAIN=local \
    CGO_ENABLED=0

WORKDIR /src
COPY go.mod ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN mkdir -p /out && \
    go build -trimpath -ldflags="-s -w" -o /out/agenthub-server ./cmd/agenthub-server

# ---------- runtime ----------
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -g 10001 agenthub && \
    adduser -D -u 10001 -G agenthub agenthub && \
    mkdir -p /app/backend/static /var/lib/agenthub && \
    chown -R agenthub:agenthub /var/lib/agenthub

WORKDIR /app
COPY --from=build /out/agenthub-server /app/agenthub-server
COPY --from=web-build /backend/static /app/backend/static

# The console bundle is served by the Go process; the snapshot keeps the
# registry across restarts when AGENTHUB_DATA_FILE points into /var/lib/agenthub.
# dev is the safe default: `AGENTHUB_ENV=prod` additionally requires
# AGENTHUB_ADMIN_TOKEN (config.Validate refuses an open production deployment).
ENV AGENTHUB_HTTP_PORT=3500 \
    AGENTHUB_ENV=dev \
    AGENTHUB_STATIC_DIR=/app/backend/static \
    AGENTHUB_DATA_FILE=/var/lib/agenthub/registry.json

USER agenthub
EXPOSE 3500

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=5 \
    CMD wget -qO- http://127.0.0.1:3500/healthz >/dev/null || exit 1

ENTRYPOINT ["/app/agenthub-server"]
