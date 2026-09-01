# Build the manager binary
FROM golang:1.27.0 AS builder

WORKDIR /workspace

# Copy the Go Modules manifests
COPY api/go.mod api/go.mod
COPY api/go.sum api/go.sum
COPY go.work ./
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Copy the project source
COPY Makefile ./
COPY cmd/main.go cmd/main.go
COPY hack/ hack/
COPY api/ api/
COPY internal/controller/ internal/controller/
COPY config/ config/
COPY pkg/ pkg/
COPY service/ service/

# Build
RUN --mount=type=cache,target=/go/pkg/mod make go-build

FROM registry.access.redhat.com/ubi9/ubi-minimal
WORKDIR /
COPY --from=builder /workspace/bin/ocs-client-operator .
COPY --from=builder /workspace/bin/status-reporter .
USER 65532:65532

ENTRYPOINT ["/ocs-client-operator"]
