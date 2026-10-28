# Makefile for the agenthub-operator module.
#
# Everything here runs inside the operator/ directory and only touches the
# operator module — the parent agenthub module (and its zero-extra-dependency
# go.mod) is never affected.

CONTROLLER_GEN ?= ./bin/controller-gen
IMG ?= agenthub-operator:latest

.PHONY: all build run test manifests docker-build lint tidy generate vet

all: build

## tidy: resolve module dependencies
tidy:
	go mod tidy

## generate: regenerate deepcopy (alias of manifests)
generate: manifests

## manifests: regenerate CRD, RBAC and deepcopy via controller-gen
manifests:
	$(CONTROLLER_GEN) object:headerFile="" paths="./api/..."
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:dir=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=agenthub-operator paths="./internal/..." output:rbac:dir=config/rbac
	@# Keep the Helm chart's bundled CRD in sync with the generated one.
	@if [ -f config/crd/bases/agenthub.ai_agenthubs.yaml ]; then \
		cp config/crd/bases/agenthub.ai_agenthubs.yaml deploy/agenthub-operator/crds/agenthub.yaml; \
	fi

## build: compile the operator binary into ./bin
build:
	go build -o bin/agenthub-operator ./cmd/agenthub-operator

## run: run the operator locally against the current kubeconfig
run: manifests
	go run ./cmd/agenthub-operator

## test: run unit tests
test:
	go test ./... -coverprofile=cover.out

## vet: go vet the module
vet:
	go vet ./...

## lint: vet (kept as a separate target for CI ergonomics)
lint: vet

## docker-build: build the operator container image
docker-build:
	docker build -t $(IMG) .
