PROJECT_DIR := $(PWD)
BIN_DIR := $(PROJECT_DIR)/bin

GOROOT ?= $(shell go env GOROOT)
GOBIN ?= $(BIN_DIR)
GOOS ?= linux
GOARCH ?= amd64

# Match the machine running tests, not the exported release GOOS/GOARCH.
ENVTEST_K8S_VERSION ?= 1.28.3
ENVTEST_OS := $(shell unset GOOS GOARCH && go env GOOS)
ENVTEST_ARCH := $(shell unset GOOS GOARCH && go env GOARCH)
ENVTEST_ASSETS := $(BIN_DIR)/k8s/$(ENVTEST_K8S_VERSION)-$(ENVTEST_OS)-$(ENVTEST_ARCH)
ENVTEST_ASSET_URL := https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v$(ENVTEST_K8S_VERSION)/envtest-v$(ENVTEST_K8S_VERSION)-$(ENVTEST_OS)-$(ENVTEST_ARCH).tar.gz

GO_LINT_IMG_LOCATION ?= golangci/golangci-lint
GO_LINT_IMG_TAG ?= v1.54.2
GO_LINT_IMG ?= $(GO_LINT_IMG_LOCATION):$(GO_LINT_IMG_TAG)
