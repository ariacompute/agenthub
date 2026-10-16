BINARY := agenthub-server
WEB := web
STATIC := backend/static

# Version stamping: /healthz and the Agent Card report config.Version, which is
# a var so the linker can overwrite it. `git describe` yields the most recent
# tag (v0.2.0), then the short commit when the tag is unreachable, and appends
# "-dirty" for a modified tree; "dev" is the last resort without git metadata.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X agenthub/internal/config.Version=$(VERSION)
export VERSION

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

# Show the version the next build will carry.
version:
	@echo $(VERSION)

# Build the image with the same version stamp (see the VERSION ARG in Dockerfile).
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t agenthub:$(VERSION) .

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
