.EXPORT_ALL_VARIABLES:
include hack/make-project-vars.mk
include hack/make-tools.mk
include hack/make-bundle-vars.mk

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.DEFAULT_GOAL := help

all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

help: ## Display this help.
	@./hack/make-help.sh $(MAKEFILE_LIST)

##@ Development

manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases

generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	go vet ./...

lint: ## Run golangci-lint against code.
	$(IMAGE_BUILD_CMD) run --rm -v $(PROJECT_DIR):/app -w /app $(GO_LINT_IMG) golangci-lint run ./...

godeps-update:  ## Run go mod tidy & vendor
	@echo "Running godeps-update"
	go mod tidy && go mod vendor
	@echo "Running godeps-update on api submodule"
	cd api && go mod tidy && go mod vendor

godeps-verify: godeps-update
	@echo "Verifying go-deps"
	./hack/godeps-verify.sh

test-setup: generate fmt vet envtest ## Run setup targets for tests

go-test: ## Run go test against code.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(BIN_DIR) -p path)" go test -coverprofile cover.out `go list ./... | grep -v "e2e"`

test: test-setup go-test ## Run go unit tests.

OCO_OPERATOR_INSTALL ?= true
OCO_OPERATOR_UNINSTALL ?= true
e2e-test: ginkgo ## TODO: Run end to end functional tests.
	@echo "build and run e2e tests"

##@ Build

build: container-build ## Build manager binary

go-build: ## Run go build against code.
	@GOBIN=${GOBIN} ./hack/go-build.sh

run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

container-build: test ## Build container image with the manager.
	$(IMAGE_BUILD_CMD) build --platform="linux/amd64" -t ${IMG} .

container-push: ## Push container image with the manager.
	$(IMAGE_BUILD_CMD) push ${IMG}

##@ Deployment

install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	cd config/console && $(KUSTOMIZE) edit set image ocs-client-operator-console=$(OCS_CLIENT_CONSOLE_IMG)
	$(KUSTOMIZE) build config/default | sed "s|STATUS_REPORTER_IMAGE_VALUE|$(IMG)|g" | awk '{print}' | kubectl apply -f -

remove: ## Remove controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete -f -

deploy-with-olm: kustomize ## Deploy controller to the K8s cluster via OLM
	cd config/install && $(KUSTOMIZE) edit set image catalog-img=${CATALOG_IMG}
	$(KUSTOMIZE) build config/install | sed "s/ocs-client-operator.v.*/ocs-client-operator.v${VERSION}/g" | kubectl create -f -

remove-with-olm: ## Remove controller from the K8s cluster
	$(KUSTOMIZE) build config/install | kubectl delete -f -

.PHONY: bundle
bundle: manifests kustomize operator-sdk yq ## Generate bundle manifests and metadata, then validate generated files.
	rm -rf ./bundle
	$(OPERATOR_SDK) generate kustomize manifests -q
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	cd config/console && $(KUSTOMIZE) edit set image ocs-client-operator-console=$(OCS_CLIENT_CONSOLE_IMG) && \
		$(KUSTOMIZE) edit set nameprefix $(OPERATOR_NAMEPREFIX)
	cd config/default && \
		$(KUSTOMIZE) edit set namespace $(OPERATOR_NAMESPACE) && \
		$(KUSTOMIZE) edit set nameprefix $(OPERATOR_NAMEPREFIX)
	cd config/manifests/bases && \
		$(KUSTOMIZE) edit add annotation --force 'olm.skipRange':"$(SKIP_RANGE)" && \
		$(KUSTOMIZE) edit add patch --name ocs-client-operator.v0.0.0 --kind ClusterServiceVersion\
		--patch '[{"op": "replace", "path": "/spec/replaces", "value": "$(REPLACES)"}]'
	$(KUSTOMIZE) build $(MANIFEST_PATH) | sed "s|STATUS_REPORTER_IMAGE_VALUE|$(IMG)|g" | awk '{print}'| \
		$(OPERATOR_SDK) generate bundle -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS) --extra-service-accounts="$$($(KUSTOMIZE) build $(MANIFEST_PATH) | $(YQ) 'select(.kind == "ServiceAccount") | .metadata.name' -N | paste -sd "," -)"
	yq -i '.dependencies[0].value.packageName = "'${CSI_ADDONS_PACKAGE_NAME}'"' config/metadata/dependencies.yaml
	yq -i '.dependencies[0].value.version = ">='${CSI_ADDONS_PACKAGE_VERSION}'"' config/metadata/dependencies.yaml
	yq -i '.dependencies[1].value.version = ">='${CEPH_CSI_PACKAGE_VERSION}'"' config/metadata/dependencies.yaml
	yq -i '.dependencies[3].value.version = ">='${SNAPSHOT_CONTROLLER_PACKAGE_VERSION}'"' config/metadata/dependencies.yaml
	cp config/metadata/* bundle/metadata/
	./hack/create-csi-images-manifest.sh
	$(OPERATOR_SDK) bundle validate ./bundle
	hack/update-csv-timestamp.sh

.PHONY: bundle-build
bundle-build: bundle ## Build the bundle image.
	$(IMAGE_BUILD_CMD) build --platform="linux/amd64" -f bundle.Dockerfile -t $(BUNDLE_IMG) .

.PHONY: bundle-push
bundle-push: ## Push the bundle image.
	$(IMAGE_BUILD_CMD) push $(BUNDLE_IMG)

# Build a catalog image by adding bundle images to an empty catalog using the operator package manager tool, 'opm'.
# This recipe invokes 'opm' in 'semver' bundle add mode. For more information on add modes, see:
# https://github.com/operator-framework/community-operators/blob/7f1438c/docs/packaging-operator.md#updating-your-existing-operator
.PHONY: catalog-build
catalog-build: opm ## Build a catalog image.
	./hack/build-catalog.sh

# Push the catalog image.
.PHONY: catalog-push
catalog-push: ## Push a catalog image.
	$(IMAGE_BUILD_CMD) push $(CATALOG_IMG)
