BINARY := agenthub-server
WEB := web
STATIC := backend/static

.PHONY: run build test vet fmt web-install web-dev web-build web-test web-test-watch clean

run:
	go run ./cmd/$(BINARY)

build: web-build
	go build -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal ./tests

web-install:
	cd $(WEB) && bun install

web-dev:
	cd $(WEB) && bun run dev

web-build: web-install
	cd $(WEB) && bun run build
	mkdir -p $(STATIC)

web-test: web-install
	cd $(WEB) && bunx vitest run

clean:
	rm -rf bin $(STATIC) $(WEB)/dist
