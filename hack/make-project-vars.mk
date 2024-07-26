# all env from this file can be overwritten from command line if required
# with "make <target> VAR=VALUE"
include hack/build.env

PROJECT_DIR := $(PWD)
BIN_DIR := $(PROJECT_DIR)/bin

GOROOT ?= $(shell go env GOROOT)
GOBIN ?= $(BIN_DIR)
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

GO_LINT_IMG_LOCATION ?= golangci/golangci-lint
GO_LINT_IMG ?= $(GO_LINT_IMG_LOCATION):$(GO_LINT_IMG_TAG)

ENVTEST_K8S_VERSION?=1.26

ifeq ($(IMAGE_BUILD_CMD),)
IMAGE_BUILD_CMD := $(shell command -v docker || echo "")
endif

ifeq ($(IMAGE_BUILD_CMD),)
IMAGE_BUILD_CMD := $(shell command -v podman || echo "")
endif
