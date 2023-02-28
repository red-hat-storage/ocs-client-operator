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

csi-images-manifest: ## Generates the YAML manifest of CSI images for each supported environment.
	./hack/gen-csi-images-manifest.sh

verify-csi-images-manifest: csi-images-manifest ## Verify csi-images-manifest has been run, if required.
	@if [[ -n "$$(git status --porcelain $${CSI_IMAGES_MANIFEST})" ]]; then \
		echo -e "\n\033[1;31mError:\033[0m Uncommitted changes to CSI images manifest found. Run \033[1m'make csi-images-manifest'\033[0m and commit the results.\n"; \
		git diff -u $${CSI_IMAGES_MANIFEST}; \
		exit 1; \
	fi

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	go vet ./...

lint: ## Run golangci-lint against code.
	docker run --rm -v $(PROJECT_DIR):/app:Z -w /app $(GO_LINT_IMG) golangci-lint run ./...

godeps-update:  ## Run go mod tidy & vendor
	go mod tidy && go mod vendor

test-setup: godeps-update generate fmt vet ## Run setup targets for tests

go-test: ## Run go test against code.
	./hack/go-test.sh

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
	go run ./main.go

container-build: test-setup ## Build container image with the manager.
	docker build -t ${IMG} .

container-push: ## Push container image with the manager.
	docker push ${IMG}

##@ Deployment

install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	cd config/default && $(KUSTOMIZE) edit set image rbac-proxy=$(RBAC_PROXY_IMG)
	$(KUSTOMIZE) build config/default | sed "s|STATUS_REPORTER_IMAGE_VALUE|$(IMG)|g" | awk '{print}' | kubectl apply -f -

remove: ## Remove controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete -f -

deploy-with-olm: kustomize ## Deploy controller to the K8s cluster via OLM
	cd config/install && $(KUSTOMIZE) edit set image catalog-img=${CATALOG_IMG}
	$(KUSTOMIZE) build config/install | sed "s/ocs-client-operator.v.*/ocs-client-operator.v${VERSION}/g" | kubectl create -f -

remove-with-olm: ## Remove controller from the K8s cluster
	$(KUSTOMIZE) build config/install | kubectl delete -f -

.PHONY: bundle
bundle: manifests kustomize operator-sdk ## Generate bundle manifests and metadata, then validate generated files.
	$(OPERATOR_SDK) generate kustomize manifests -q
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	cd config/default && \
		$(KUSTOMIZE) edit set image rbac-proxy=$(RBAC_PROXY_IMG)
	cd config/manifests/bases && \
		$(KUSTOMIZE) edit add annotation --force 'olm.skipRange':"$(SKIP_RANGE)" && \
		$(KUSTOMIZE) edit add patch --name ocs-client-operator.v0.0.0 --kind ClusterServiceVersion\
		--patch '[{"op": "replace", "path": "/spec/replaces", "value": "$(REPLACES)"}]'
	$(KUSTOMIZE) build $(MANIFEST_PATH) | sed "s|STATUS_REPORTER_IMAGE_VALUE|$(IMG)|g" | awk '{print}'| \
		$(OPERATOR_SDK) generate bundle -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS) --extra-service-accounts=ocs-client-operator-csi-cephfs-provisioner-sa,ocs-client-operator-csi-cephfs-plugin-sa,ocs-client-operator-csi-rbd-provisioner-sa,ocs-client-operator-csi-rbd-plugin-sa,ocs-client-operator-status-reporter
	sed -i "s|packageName:.*|packageName: ${CSI_ADDONS_PACKAGE_NAME}|g" "config/metadata/dependencies.yaml"
	sed -i "s|version:.*|version: "${CSI_ADDONS_PACKAGE_VERSION}"|g" "config/metadata/dependencies.yaml"
	cp config/metadata/* bundle/metadata/
	$(OPERATOR_SDK) bundle validate ./bundle

.PHONY: bundle-build
bundle-build: bundle ## Build the bundle image.
	docker build -f bundle.Dockerfile -t $(BUNDLE_IMG) .

.PHONY: bundle-push
bundle-push: ## Push the bundle image.
	docker push $(BUNDLE_IMG)

# Build a catalog image by adding bundle images to an empty catalog using the operator package manager tool, 'opm'.
# This recipe invokes 'opm' in 'semver' bundle add mode. For more information on add modes, see:
# https://github.com/operator-framework/community-operators/blob/7f1438c/docs/packaging-operator.md#updating-your-existing-operator
.PHONY: catalog-build
catalog-build: opm ## Build a catalog image.
	$(OPM) index add --permissive --container-tool docker --mode semver --tag $(CATALOG_IMG) --bundles $(BUNDLE_IMGS) $(FROM_INDEX_OPT)

# Push the catalog image.
.PHONY: catalog-push
catalog-push: ## Push a catalog image.
	docker push $(CATALOG_IMG)
