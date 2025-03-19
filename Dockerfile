# Build the manager binary
FROM golang:1.23 as builder

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.sum ./
# cache deps before building and copying source so that we don't need to re-build as much
# and so that source changes don't invalidate our built layer
COPY vendor/ vendor/

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
RUN make go-build

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM registry.access.redhat.com/ubi9/ubi-minimal
WORKDIR /
COPY --from=builder /workspace/bin/manager .
COPY --from=builder /workspace/bin/status-reporter .
COPY --from=builder /workspace/hack/entrypoint.sh entrypoint
USER 65532:65532

ENTRYPOINT ["/entrypoint"]
