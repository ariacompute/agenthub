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

# Local compose database (host port 5434 avoids postgres 5432 / bankmind 5433).
DATABASE_URL ?= postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable
export DATABASE_URL

.PHONY: run build test test-integration vet fmt version docker-build \
        db-up db-down db-psql \
        web-install web-dev web-build web-test web-test-watch clean

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
docker-build:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
	  --build-arg DISTANCE=$(DISTANCE) --build-arg DIRTY=$(DIRTY) \
	  -t agenthub:$(VERSION) .

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

clean:
	rm -rf bin $(STATIC) $(WEB)/dist
