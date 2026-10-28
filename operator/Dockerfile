# agenthub-operator — the kubernetes controller binary.
#
# Stage 1 compiles a static controller-runtime manager; stage 2 drops it onto
# a distroless non-root image. The operator only talks to the Kubernetes API,
# so it needs no shell, package manager or CA bundle beyond what distroless
# already ships.
FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY internal ./internal
COPY cmd ./cmd

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w" \
    -o /out/agenthub-operator ./cmd/agenthub-operator

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /
COPY --from=build /out/agenthub-operator /agenthub-operator

USER 65532:65532
ENTRYPOINT ["/agenthub-operator"]
