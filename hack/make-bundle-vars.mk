# VERSION defines the project version for the bundle.
# Update this value when you upgrade the version of your project.
# To re-generate a bundle for another specific version without changing the standard setup, you can:
# - use the VERSION as arg of the bundle target (e.g make bundle VERSION=0.0.2)
# - use environment variables to overwrite this value (e.g export VERSION=0.0.2)
VERSION ?= 4.20.0

# DEFAULT_CHANNEL defines the default channel used in the bundle.
# Add a new line here if you would like to change its default config. (E.g DEFAULT_CHANNEL = "stable")
# To re-generate a bundle for any other default channel without changing the default setup, you can:
# - use the DEFAULT_CHANNEL as arg of the bundle target (e.g make bundle DEFAULT_CHANNEL=stable)
# - use environment variables to overwrite this value (e.g export DEFAULT_CHANNEL="stable")
DEFAULT_CHANNEL ?= alpha
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)

# CHANNELS define the bundle channels used in the bundle.
# Add a new line here if you would like to change its default config. (E.g CHANNELS = "preview,fast,stable")
# To re-generate a bundle for other specific channels without changing the standard setup, you can:
# - use the CHANNELS as arg of the bundle target (e.g make bundle CHANNELS=preview,fast,stable)
# - use environment variables to overwrite this value (e.g export CHANNELS="preview,fast,stable")
CHANNELS ?= $(DEFAULT_CHANNEL)
BUNDLE_CHANNELS := --channels=$(CHANNELS)

BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# Each CSV has a replaces parameter that indicates which Operator it replaces.
# This builds a graph of CSVs that can be queried by OLM, and updates can be
# shared between channels. Channels can be thought of as entry points into
# the graph of updates:
REPLACES ?=

# Creating the New CatalogSource requires publishing CSVs that replace one Operator,
# but can skip several. This can be accomplished using the skipRange annotation:
SKIP_RANGE ?=

# Set to true for generating fusion bundle
FUSION ?= false
MANIFEST_PATH=config/manifests
ifeq ($(FUSION), true)
MANIFEST_PATH=config/manifests/fusion
endif

# Image URL to use all building/pushing image targets
IMAGE_REGISTRY ?= quay.io
REGISTRY_NAMESPACE ?= ocs-dev
CSI_ADDONS_IMAGE_REGISTRY ?= $(IMAGE_REGISTRY)
CSI_ADDONS_REGISTRY_NAMESPACE ?= csiaddons
IMAGE_TAG ?= latest
IMAGE_NAME ?= ocs-client-operator
BUNDLE_IMAGE_NAME ?= $(IMAGE_NAME)-bundle
CSI_ADDONS_BUNDLE_IMAGE_NAME ?= k8s-bundle
CSI_ADDONS_BUNDLE_IMAGE_TAG ?= v0.11.0
CATALOG_IMAGE_NAME ?= $(IMAGE_NAME)-catalog

NOOBAA_BUNDLE_NAME ?= noobaa-operator
NOOBAA_BUNDLE_IMG_NAME ?= $(NOOBAA_BUNDLE_NAME)-bundle
NOOBAA_BUNDLE_VERSION ?= v5.19.0
NOOBAA_BUNDLE_IMG_TAG ?= master-20250226
NOOBAA_BUNDLE_IMG_NAMESPACE ?= noobaa
NOOBAA_BUNDLE_IMG ?= $(IMAGE_REGISTRY)/$(NOOBAA_BUNDLE_IMG_NAMESPACE)/$(NOOBAA_BUNDLE_IMG_NAME):$(NOOBAA_BUNDLE_IMG_TAG)

OCS_CLIENT_CONSOLE_IMG_NAME ?= ocs-client-console
OCS_CLIENT_CONSOLE_IMG_TAG ?= latest
OCS_CLIENT_CONSOLE_IMG_LOCATION ?= $(IMAGE_REGISTRY)/$(REGISTRY_NAMESPACE)
OCS_CLIENT_CONSOLE_IMG ?= $(OCS_CLIENT_CONSOLE_IMG_LOCATION)/$(OCS_CLIENT_CONSOLE_IMG_NAME):$(OCS_CLIENT_CONSOLE_IMG_TAG)

CEPH_CSI_BUNDLE_NAME ?= cephcsi-operator
CEPH_CSI_REGISTRY_NAMESPACE ?= ocs-dev
CEPH_CSI_BUNDLE_IMG_NAME ?= $(CEPH_CSI_BUNDLE_NAME)-bundle
CEPH_CSI_BUNDLE_VERSION ?= v4.19.0
CEPH_CSI_BUNDLE_IMG_TAG ?= v4.19.0

RECIPE_BUNDLE_NAME ?= recipe
RECIPE_REGISTRY_NAMESPACE ?= ramendr
RECIPE_BUNDLE_IMG_NAME ?= $(RECIPE_BUNDLE_NAME)-bundle
RECIPE_BUNDLE_VERSION ?= 0.0.1
RECIPE_BUNDLE_IMG_TAG ?= latest

SNAPSHOT_CONTROLLER_BUNDLE_NAME ?= snapshot-controller
SNAPSHOT_CONTROLLER_REGISTRY_NAMESPACE ?= ocs-dev
SNAPSHOT_CONTROLLER_BUNDLE_IMG_NAME ?= $(SNAPSHOT_CONTROLLER_BUNDLE_NAME)-bundle
SNAPSHOT_CONTROLLER_BUNDLE_VERSION ?= 4.20.0
SNAPSHOT_CONTROLLER_BUNDLE_IMG_TAG ?= latest

# IMG defines the image used for the operator.
IMG ?= $(IMAGE_REGISTRY)/$(REGISTRY_NAMESPACE)/$(IMAGE_NAME):$(IMAGE_TAG)

# BUNDLE_IMG defines the image used for the bundle.
BUNDLE_IMG ?= $(IMAGE_REGISTRY)/$(REGISTRY_NAMESPACE)/$(BUNDLE_IMAGE_NAME):$(IMAGE_TAG)

CSI_ADDONS_BUNDLE_IMG ?= $(CSI_ADDONS_IMAGE_REGISTRY)/$(CSI_ADDONS_REGISTRY_NAMESPACE)/$(CSI_ADDONS_BUNDLE_IMAGE_NAME):$(CSI_ADDONS_BUNDLE_IMAGE_TAG)

CEPH_CSI_BUNDLE_IMG ?= $(IMAGE_REGISTRY)/$(CEPH_CSI_REGISTRY_NAMESPACE)/$(CEPH_CSI_BUNDLE_IMG_NAME):$(CEPH_CSI_BUNDLE_IMG_TAG)

RECIPE_BUNDLE_IMG ?= $(IMAGE_REGISTRY)/$(RECIPE_REGISTRY_NAMESPACE)/$(RECIPE_BUNDLE_IMG_NAME):$(RECIPE_BUNDLE_IMG_TAG)

SNAPSHOT_CONTROLLER_BUNDLE_IMG ?= $(IMAGE_REGISTRY)/$(SNAPSHOT_CONTROLLER_REGISTRY_NAMESPACE)/$(SNAPSHOT_CONTROLLER_BUNDLE_IMG_NAME):$(SNAPSHOT_CONTROLLER_BUNDLE_IMG_TAG)

# CATALOG_IMG defines the image used for the catalog.
CATALOG_IMG ?= $(IMAGE_REGISTRY)/$(REGISTRY_NAMESPACE)/$(CATALOG_IMAGE_NAME):$(IMAGE_TAG)

# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
CRD_OPTIONS ?= "crd:generateEmbeddedObjectMeta=true"

# A comma-separated list of bundle images (e.g. make catalog-build BUNDLE_IMGS=example.com/operator-bundle:v0.1.0,example.com/operator-bundle:v0.2.0).
# These images MUST exist in a registry and be pull-able.
BUNDLE_IMGS ?= $(shell echo $(BUNDLE_IMG) $(CSI_ADDONS_BUNDLE_IMG) $(CEPH_CSI_BUNDLE_IMG) $(NOOBAA_BUNDLE_IMG) | sed "s/ /,/g")

# Set CATALOG_BASE_IMG to an existing catalog image tag to add $BUNDLE_IMGS to that image.
ifneq ($(origin CATALOG_BASE_IMG), undefined)
FROM_INDEX_OPT := --from-index $(CATALOG_BASE_IMG)
endif

# manager env variables
OPERATOR_NAMEPREFIX ?= ocs-client-operator-
OPERATOR_NAMESPACE ?= $(OPERATOR_NAMEPREFIX)system
OPERATOR_CATALOGSOURCE ?= oco-catalogsource

# kube rbac proxy image variables
CLUSTER_ENV ?= openshift
KUBE_RBAC_PROXY_IMG ?= gcr.io/kubebuilder/kube-rbac-proxy:v0.8.0
OSE_KUBE_RBAC_PROXY_IMG ?= registry.redhat.io/openshift4/ose-kube-rbac-proxy:v4.9.0

ifeq ($(CLUSTER_ENV), openshift)
	RBAC_PROXY_IMG ?= $(OSE_KUBE_RBAC_PROXY_IMG)
else ifeq ($(CLUSTER_ENV), kubernetes)
	RBAC_PROXY_IMG ?= $(KUBE_RBAC_PROXY_IMG)
endif

# csi-addons dependencies
CSI_ADDONS_PACKAGE_NAME ?= csi-addons
CSI_ADDONS_PACKAGE_VERSION ?= 0.11.0

# ceph-csi-operator dependencies
CEPH_CSI_PACKAGE_VERSION ?= 4.19.0

# noobaa dependencies
NOOBAA_PACKAGE_VERSION ?= 5.19.0

# snapshot-controller dependencies
SNAPSHOT_CONTROLLER_PACKAGE_NAME ?= odf-external-snapshotter-operator
SNAPSHOT_CONTROLLER_PACKAGE_VERSION ?= 4.20.0

# The following variables are here as a convenience for developers so we don't have
# to retype things, because we're lazy.
IMAGE_LOCATION_SIG_STORAGE ?= registry.k8s.io/sig-storage
IMAGE_LOCATION_CSI_ADDONS ?= quay.io/csiaddons
IMAGE_LOCATION_CEPH_CSI ?= quay.io/cephcsi
IMAGE_LOCATION_REDHAT_OCP ?= registry.redhat.io/openshift4
IMAGE_LOCATION_REDHAT_ODF ?= registry.redhat.io/odf4
IMAGE_LOCATION_ODF_SNAPSHOTTER ?= quay.io/ocs-dev

DEFAULT_CSI_IMG_PROVISIONER_NAME ?= csi-provisioner
DEFAULT_CSI_IMG_PROVISIONER_VERSION ?= v5.3.0
DEFAULT_CSI_IMG_ATTACHER_NAME ?= csi-attacher
DEFAULT_CSI_IMG_ATTACHER_VERSION ?= v4.9.0
DEFAULT_CSI_IMG_RESIZER_NAME ?= csi-resizer
DEFAULT_CSI_IMG_RESIZER_VERSION ?= v1.13.2
DEFAULT_CSI_IMG_SNAPSHOTTER_NAME ?= csi-snapshotter
DEFAULT_CSI_IMG_SNAPSHOTTER_VERSION ?= v8.2.0
DEFAULT_CSI_IMG_SNAPSHOT_METADATA_NAME ?= csi-snapshot-metadata
DEFAULT_CSI_IMG_SNAPSHOT_METADATA_VERSION ?= v0.1.0
DEFAULT_CSI_IMG_REGISTRAR_NAME ?= csi-node-driver-registrar
DEFAULT_CSI_IMG_REGISTRAR_VERSION ?= v2.14.0
DEFAULT_CSI_IMG_ADDONS_NAME ?= k8s-sidecar
DEFAULT_CSI_IMG_ADDONS_VERSION ?= v0.12.0
DEFAULT_CSI_IMG_CEPH_CSI_NAME ?= cephcsi
DEFAULT_CSI_IMG_CEPH_CSI_VERSION ?= v3.14.1
DEFAULT_CSI_IMG_ODF_SNAPSHOTTER_NAME ?= csi-snapshotter
DEFAULT_CSI_IMG_ODF_SNAPSHOTTER_VERSION ?= latest

CSI_IMG_PROVISIONER ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(DEFAULT_CSI_IMG_PROVISIONER_NAME):$(DEFAULT_CSI_IMG_PROVISIONER_VERSION)
CSI_IMG_ATTACHER ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(DEFAULT_CSI_IMG_ATTACHER_NAME):$(DEFAULT_CSI_IMG_ATTACHER_VERSION)
CSI_IMG_RESIZER ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(DEFAULT_CSI_IMG_RESIZER_NAME):$(DEFAULT_CSI_IMG_RESIZER_VERSION)
CSI_IMG_SNAPSHOTTER ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(DEFAULT_CSI_IMG_SNAPSHOTTER_NAME):$(DEFAULT_CSI_IMG_SNAPSHOTTER_VERSION)
CSI_IMG_SNAPSHOT_METADATA ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(DEFAULT_CSI_IMG_SNAPSHOT_METADATA_NAME):$(DEFAULT_CSI_IMG_SNAPSHOT_METADATA_VERSION)
CSI_IMG_REGISTRAR ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(DEFAULT_CSI_IMG_REGISTRAR_NAME):$(DEFAULT_CSI_IMG_REGISTRAR_VERSION)
CSI_IMG_ADDONS ?= $(IMAGE_LOCATION_CSI_ADDONS)/$(DEFAULT_CSI_IMG_ADDONS_NAME):$(DEFAULT_CSI_IMG_ADDONS_VERSION)
CSI_IMG_CEPH_CSI ?= $(IMAGE_LOCATION_CEPH_CSI)/$(DEFAULT_CSI_IMG_CEPH_CSI_NAME):$(DEFAULT_CSI_IMG_CEPH_CSI_VERSION)
CSI_IMG_ODF_SNAPSHOTTER ?= $(IMAGE_LOCATION_ODF_SNAPSHOTTER)/$(DEFAULT_CSI_IMG_ODF_SNAPSHOTTER_NAME):$(DEFAULT_CSI_IMG_ODF_SNAPSHOTTER_VERSION)

# CSI_OCP_VERSIONS is a space-delimited list of supported OpenShift
# versions. For each version, the default behavior is to use the image
# variables defined above. You can override any image for each VERSION by
# specifying a variable of the format: CSI_IMG_<CONTAINER>_<VERSION>, where
# VERSION has "." replaced with "_". These values can be any valid container
# image name or URL, and the use of the above convenience variables is entirely
# optional.
#
# Example:
#   CSI_OCP_VERSIONS ?= v4.x
#   CSI_IMG_PROVISIONER_v4_x ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(CSI_IMG_PROVISIONER_NAME):v1
#   CSI_IMG_ATTACHER_v4_x ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(CSI_IMG_ATTACHER_NAME):v1
#   CSI_IMG_RESIZER_v4_x ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(CSI_IMG_RESIZER_NAME_NAME):v1
#   CSI_IMG_SNAPSHOTTER_v4_x ?= $(IMAGE_LOCATION_SIG_STORAGE)/$(CSI_IMG_SNAPSHOTTER_NAME):v1
#   CSI_IMG_REGISTRAR_v4_x ?= $(CSI_IMG_REGISTRAR)
#   CSI_IMG_ADDONS_v4_x ?= quay.io/csiaddons/k8s-sidecar:v3
#   CSI_IMG_CEPH_CSI_v4_x ?= cephcsi:v0.1
#   CSI_IMG_ODF_SNAPSHOTTER_v4_x ?= $(IMAGE_LOCATION_ODF_SNAPSHOTTER)/$(CSI_IMG_ODF_SNAPSHOTTER_NAME):v1

# we will maintain N (VERSION var in this file) through and including N-2 versions
CSI_OCP_VERSIONS ?= v4.18 v4.19 v4.20
