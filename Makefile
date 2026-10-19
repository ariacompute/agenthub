BINARY := agenthub-server
WEB := web
STATIC := backend/static

# Build stamping: /healthz and the console report "version@commit", e.g.
# v0.2.0@1a2b3c4. Both are vars so the linker can overwrite them.
# VERSION is the git tag verbatim (prefix included); COMMIT is the seven
# character revision. "dev" / "unknown" are the last-resort fallbacks when git
# metadata is unavailable, so an unstamped build is always obvious.
VERSION ?= $(shell git describe --tags --abbrev=0 --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null | cut -c1-7)
ifeq ($(COMMIT),)
COMMIT := unknown
endif
# How far HEAD sits after the tag: 0 means the build is exactly at the tag, so a
# "+n" in the console shows a binary that has not been released yet.
DISTANCE ?= $(shell if [ -n "$(shell git describe --tags --abbrev=0 2>/dev/null)" ]; then \
                     git rev-list --count $(shell git describe --tags --abbrev=0 2>/dev/null)..HEAD 2>/dev/null || echo 0; \
                   else echo 0; fi)
# "true" when the tree carried uncommitted changes, so a local build can never
# be mistaken for a reproducible release.
DIRTY ?= $(shell if [ -n "$$(git status --porcelain 2>/dev/null)" ]; then echo true; else echo false; fi)
LDFLAGS := -s -w \
	-X agenthub/internal/config.Version=$(VERSION) \
	-X agenthub/internal/config.Commit=$(COMMIT) \
	-X agenthub/internal/config.Distance=$(DISTANCE) \
	-X agenthub/internal/config.Dirty=$(DIRTY)
export VERSION
export COMMIT
export DISTANCE
export DIRTY

# Image build: the Dockerfile fetches npm packages and Go modules, so it needs
# outbound network. BUILD_PROXY defaults to whatever the shell already exports
# ($HTTP_PROXY) — with --network=host the build container can then reach a
# host-side proxy on 127.0.0.1. Leave it empty for a direct connection.
BUILD_PROXY ?= $(HTTP_PROXY)
GOPROXY ?= https://goproxy.cn,direct
# Empty leaves the Dockerfile's default (the Alpine CDN) in place; set it when
# that host is unreachable, e.g. ALPINE_MIRROR=https://mirrors.aliyun.com
ALPINE_MIRROR ?=

# Local compose database (host port 5434 avoids postgres 5432 / bankmind 5433).
DATABASE_URL ?= postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable
export DATABASE_URL

.PHONY: run build test test-integration test-cover vet fmt version up down ports \
        docker-build compose-build \
        db-up db-down db-psql \
        web-install web-dev web-build web-test web-test-watch clean \
        desktop-install desktop-dev desktop-build desktop-test

run: db-up
	go run -ldflags="$(LDFLAGS)" ./cmd/$(BINARY)

build: web-build
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

# Unit + integration: integration cases skip themselves when DATABASE_URL is unset.
test:
	go test -race ./...

# Integration only, against the compose database (or $DATABASE_URL).
test-integration:
	go test -race -count=1 -run Integration ./tests/...

# Per package coverage. The store package needs DATABASE_URL to measure: its
# cases skip without one, which is honest but leaves the number near zero.
test-cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	@printf '\nper package:\n'
	@go test -count=1 -cover ./... | grep -v 'no test files' || true

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal ./tests

# Show the build string the next binary will carry, e.g. "v0.2.0@1a2b3c4 +2 dirty".
version:
	@printf '%s@%s' "$(VERSION)" "$(COMMIT)"; \
	if [ "$(DISTANCE)" != "0" ]; then printf ' +%s' "$(DISTANCE)"; fi; \
	if [ "$(DIRTY)" = "true" ]; then printf ' dirty'; fi; \
	echo

# Build the image with the same stamp (see the VERSION / COMMIT / DISTANCE /
# DIRTY args in Dockerfile). Images are built from a clean checkout by CI, so
# DISTANCE / DIRTY default to 0 / false there.
# --network=host is what lets BUILD_PROXY's 127.0.0.1 resolve to the host.
docker-build:
	docker build --network=host \
	  --build-arg BUILD_PROXY=$(BUILD_PROXY) --build-arg GOPROXY=$(GOPROXY) \
	  --build-arg ALPINE_MIRROR=$(ALPINE_MIRROR) \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
	  --build-arg DISTANCE=$(DISTANCE) --build-arg DIRTY=$(DIRTY) \
	  -t agenthub:$(VERSION) .

# Same image, built through compose, with the git stamp forwarded (compose itself
# cannot read git metadata). The build runs on the bridge network, where a
# host-side BUILD_PROXY (127.0.0.1) does not resolve; the Dockerfile falls back
# to a direct download in that case, so this succeeds with or without a proxy.
compose-build:
	VERSION=$(VERSION) COMMIT=$(COMMIT) DISTANCE=$(DISTANCE) DIRTY=$(DIRTY) \
	BUILD_PROXY=$(BUILD_PROXY) GOPROXY=$(GOPROXY) ALPINE_MIRROR=$(ALPINE_MIRROR) \
	docker compose build agenthub

# Start the composed stack and report what each published port answers. Compose
# has no post-up hook, so the summary is printed right after `up -d`
# (scripts/compose-ports.sh); `make ports` prints the same table later, and
# `docker compose up` (attached) shows it in the server log instead.
up:
	docker compose up -d
	@scripts/compose-ports.sh

# Port map of an already running stack: service → host → container → purpose.
ports:
	@scripts/compose-ports.sh

# Stop without losing data: everything lives in the agenthub-pgdata volume.
down:
	docker compose down

db-up:
	docker compose up -d postgres
	@echo "waiting for postgres to accept connections on $(DATABASE_URL)"
	@for i in $$(seq 1 30); do \
		docker compose exec -T postgres pg_isready -q -U agenthub && exit 0; \
		sleep 1; \
	done; \
	echo "postgres did not become ready in time"; exit 1

db-down:
	docker compose down

db-psql:
	docker compose exec postgres psql -U agenthub -d agenthub

web-install:
	cd $(WEB) && bun install

web-dev:
	cd $(WEB) && bun run dev

web-build: web-install
	cd $(WEB) && bun run build
	mkdir -p $(STATIC)

web-test: web-install
	cd $(WEB) && bunx vitest run

web-test-watch:
	cd $(WEB) && bunx vitest

# Desktop shell (Tauri v2, Rust). It has no backend of its own: it opens the
# console against a server the user points it at. `desktop-build` therefore
# builds the console first — the shell embeds `backend/static` at compile time.
DESKTOP := desktop

desktop-install:
	cd $(DESKTOP) && bun install

# The console is embedded at compile time, so it has to exist before either
# `tauri dev` or `tauri build` runs. Building it here rather than through
# beforeDevCommand / beforeBuildCommand keeps the shell out of it: one command
# that behaves the same on Linux, macOS and Windows.
desktop-dev: desktop-install web-build
	cd $(DESKTOP) && bun run dev

desktop-build: desktop-install web-build
	cd $(DESKTOP) && bun run build

desktop-test: desktop-install
	cd $(DESKTOP)/src-tauri && cargo test

clean:
	rm -rf bin $(STATIC) $(WEB)/dist coverage.out
