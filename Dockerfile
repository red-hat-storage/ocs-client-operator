# Build the manager binary
FROM golang:1.20 as builder

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.sum ./
# cache deps before building and copying source so that we don't need to re-build as much
# and so that source changes don't invalidate our built layer
COPY vendor/ vendor/

# Copy the project source
COPY main.go Makefile ./
COPY hack/ hack/
COPY api/ api/
COPY controllers/ controllers/
COPY config/ config/
COPY pkg/ pkg/
COPY service/ service/
# Run tests and linting
RUN make go-test

# Build
RUN make go-build

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/manager .
COPY --from=builder /workspace/bin/status-reporter .
USER 65532:65532

ENTRYPOINT ["/manager"]
