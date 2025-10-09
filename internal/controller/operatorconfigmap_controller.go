/*
Copyright 2023 Red Hat OpenShift Data Foundation.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	// The embed package is required for the prometheus rule files
	_ "embed"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/console"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	nbv1 "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	configv1 "github.com/openshift/api/config/v1"
	secv1 "github.com/openshift/api/security/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	admrv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/util/yaml"
	k8sYAML "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

//go:embed pvc-rules.yaml
var pvcPrometheusRules string

const (
	operatorConfigMapName = "ocs-client-operator-config"
	// ClusterVersionName is the name of the ClusterVersion object in the
	// openshift cluster.
	clusterVersionName                 = "version"
	manageNoobaaSubKey                 = "manageNoobaaSubscription"
	disableVersionChecksKey            = "disableVersionChecks"
	disableInstallPlanAutoApprovalKey  = "disableInstallPlanAutoApproval"
	subscriptionLabelKey               = "managed-by"
	subscriptionLabelValue             = "webhook.subscription.ocs.openshift.io"
	generateRbdOMapInfoKey             = "generateRbdOMapInfo"
	useHostNetworkForCsiControllersKey = "useHostNetworkForCsiControllers"

	operatorConfigMapFinalizer = "ocs-client-operator.ocs.openshift.io/storageused"
	subPackageIndexName        = "index:subscriptionPackage"
	csiImagesConfigMapLabel    = "ocs.openshift.io/csi-images-version"
	cniNetworksAnnotationKey   = "k8s.v1.cni.cncf.io/networks"
	noobaaCrdName              = "noobaas.noobaa.io"
	noobaaCrName               = "noobaa-remote"
)

// OperatorConfigMapReconciler reconciles a ClusterVersion object
type OperatorConfigMapReconciler struct {
	client.Client
	OperatorNamespace string
	ConsolePort       int32
	Scheme            *runtime.Scheme
	AvailableCrds     map[string]bool

	log                 logr.Logger
	ctx                 context.Context
	operatorConfigMap   *corev1.ConfigMap
	consoleDeployment   *appsv1.Deployment
	subscriptionChannel string
}

// SetupWithManager sets up the controller with the Manager.
func (c *OperatorConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	if err := mgr.GetCache().IndexField(ctx, &opv1a1.Subscription{}, subPackageIndexName, func(o client.Object) []string {
		if sub := o.(*opv1a1.Subscription); sub != nil {
			return []string{sub.Spec.Package}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("unable to set up FieldIndexer for subscription package name: %v", err)
	}

	clusterVersionPredicates := builder.WithPredicates(
		predicate.GenerationChangedPredicate{},
	)

	configMapPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				namespace := client.GetNamespace()
				name := client.GetName()
				return ((namespace == c.OperatorNamespace) && (name == operatorConfigMapName))
			},
		),
	)
	// Reconcile the OperatorConfigMap object when the cluster's version object is updated
	enqueueConfigMapRequest := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, _ client.Object) []reconcile.Request {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Name:      operatorConfigMapName,
					Namespace: c.OperatorNamespace,
				},
			}}
		},
	)

	subscriptionPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				return client.GetNamespace() == c.OperatorNamespace
			},
		),
		predicate.LabelChangedPredicate{},
	)

	webhookPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				return client.GetName() == templates.SubscriptionWebhookName
			},
		),
	)

	servicePredicate := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(obj client.Object) bool {
				return obj.GetNamespace() == c.OperatorNamespace && obj.GetName() == templates.WebhookServiceName
			},
		),
	)

	generationChangePredicate := predicate.GenerationChangedPredicate{}
	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, configMapPredicates).
		Owns(&corev1.Service{}, servicePredicate).
		Owns(&csiopv1a1.OperatorConfig{}, builder.WithPredicates(generationChangePredicate)).
		Owns(&csiopv1a1.Driver{}, builder.WithPredicates(generationChangePredicate)).
		Watches(&configv1.ClusterVersion{}, enqueueConfigMapRequest, clusterVersionPredicates).
		Watches(
			&extv1.CustomResourceDefinition{},
			enqueueConfigMapRequest,
			builder.WithPredicates(
				utils.NamePredicate(MaintenanceModeCRDName),
				utils.EventTypePredicate(
					!c.AvailableCrds[MaintenanceModeCRDName],
					false,
					true,
					false,
				),
			),
			builder.OnlyMetadata,
		).
		Watches(&opv1a1.Subscription{}, enqueueConfigMapRequest, subscriptionPredicates).
		Watches(
			&opv1a1.InstallPlan{},
			enqueueConfigMapRequest,
			builder.WithPredicates(
				utils.EventTypePredicate(
					true,
					false,
					false,
					false,
				),
			),
		).
		Watches(&admrv1.ValidatingWebhookConfiguration{}, enqueueConfigMapRequest, webhookPredicates).
		Watches(&v1alpha1.StorageClient{}, enqueueConfigMapRequest, builder.WithPredicates(predicate.AnnotationChangedPredicate{}))

	if c.AvailableCrds[noobaaCrdName] {
		bldr.Watches(
			&nbv1.NooBaa{},
			enqueueConfigMapRequest,
			builder.WithPredicates(
				utils.NamePredicate(noobaaCrName),
				predicate.GenerationChangedPredicate{},
			),
		)
	}

	return bldr.Complete(c)
}

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch
//+kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch
//+kubebuilder:rbac:groups="apps",resources=deployments/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=configmaps/finalizers,verbs=update
//+kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=get;list;watch;create;patch;update;delete
//+kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list;watch;create;update
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=console.openshift.io,resources=consoleplugins,verbs=*
//+kubebuilder:rbac:groups=operators.coreos.com,resources=subscriptions,verbs=get;list;watch;update;delete
//+kubebuilder:rbac:groups=operators.coreos.com,resources=installplans,verbs=get;list;watch;patch
//+kubebuilder:rbac:groups=operators.coreos.com,resources=clusterserviceversions,verbs=delete;list
//+kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=csi.ceph.io,resources=operatorconfigs,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=csi.ceph.io,resources=drivers,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=noobaa.io,resources=noobaas,verbs=get;list;watch;update;delete

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (c *OperatorConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c.ctx = ctx
	c.log = log.FromContext(ctx, "OperatorConfigMap", req)
	c.log.Info("Reconciling OperatorConfigMap")

	crd := &metav1.PartialObjectMetadata{}
	crd.SetGroupVersionKind(extv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	crd.Name = MaintenanceModeCRDName
	if err := c.Client.Get(ctx, client.ObjectKeyFromObject(crd), crd); client.IgnoreNotFound(err) != nil {
		c.log.Error(err, "Failed to get CRD", "CRD", crd.Name)
		return reconcile.Result{}, err
	}
	utils.AssertEqual(c.AvailableCrds[crd.Name], crd.UID != "", utils.ExitCodeThatShouldRestartTheProcess)

	c.operatorConfigMap = &corev1.ConfigMap{}
	c.operatorConfigMap.Name = req.Name
	c.operatorConfigMap.Namespace = req.Namespace
	if err := c.get(c.operatorConfigMap); err != nil {
		if kerrors.IsNotFound(err) {
			c.log.Info("Operator ConfigMap resource not found. Ignoring since object might be deleted.")
			return reconcile.Result{}, nil
		}
		c.log.Error(err, "failed to get the operator's configMap")
		return reconcile.Result{}, err
	}

	storageClients := &v1alpha1.StorageClientList{}
	if err := c.list(storageClients); err != nil {
		c.log.Error(err, "failed to list StorageClients")
		return reconcile.Result{}, err
	}

	disableVersionChecks, err := strconv.ParseBool(cmp.Or(c.operatorConfigMap.Data[disableVersionChecksKey], "false"))
	if err != nil {
		c.log.Error(err, "failed to parse configmap key data", "key", disableVersionChecksKey)
	}
	if !disableVersionChecks {
		if c.subscriptionChannel, err = c.getDesiredSubscriptionChannel(storageClients); err != nil {
			return reconcile.Result{}, err
		}
	}

	if c.operatorConfigMap.GetDeletionTimestamp().IsZero() {

		//ensure finalizer
		if controllerutil.AddFinalizer(c.operatorConfigMap, operatorConfigMapFinalizer) {
			c.log.Info("finalizer missing on the operatorConfigMap resource, adding...")
			if err := c.Client.Update(c.ctx, c.operatorConfigMap); err != nil {
				return ctrl.Result{}, err
			}
		}

		if disableVersionChecks {
			// delete the webhook if it exists
			whConfig := &admrv1.ValidatingWebhookConfiguration{}
			whConfig.Name = templates.SubscriptionWebhookName
			if err := c.get(whConfig); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			} else if whConfig.UID != "" {
				if err = c.delete(whConfig); err != nil {
					return ctrl.Result{}, err
				}
			}
		} else {
			if err := c.reconcileWebhookService(); err != nil {
				c.log.Error(err, "unable to reconcile webhook service")
				return ctrl.Result{}, err
			}

			if err := c.reconcileSubscriptionValidatingWebhook(); err != nil {
				c.log.Error(err, "unable to register subscription validating webhook")
				return ctrl.Result{}, err
			}
		}

		if err := c.reconcileClientOperatorSubscription(); err != nil {
			c.log.Error(err, "unable to reconcile client operator subscription")
			return ctrl.Result{}, err
		}

		//don't reconcile noobaa-operator for remote clusters
		if false {
			if c.getNoobaaSubManagementConfig() {
				if err := c.reconcileNoobaaOperatorSubscription(); err != nil {
					c.log.Error(err, "unable to reconcile Noobaa Operator subscription")
					return ctrl.Result{}, err
				}
			}
		}

		// remove noobaa resources installed by older version of Client
		if err := c.removeNoobaa(); err != nil {
			c.log.Error(err, "unable to remove Noobaa")
			return ctrl.Result{}, err
		}

		if err := c.removeNoobaaOperator(); err != nil {
			c.log.Error(err, "unable to remove Noobaa Operator subscription")
			return ctrl.Result{}, err
		}

		if err := c.reconcileCSIAddonsOperatorSubscription(); err != nil {
			c.log.Error(err, "unable to reconcile CSI Addons subscription")
			return ctrl.Result{}, err
		}

		if err := c.reconcileCephCSIOperatorSubscription(); err != nil {
			c.log.Error(err, "unable to reconcile Ceph CSI Operator subscription")
			return ctrl.Result{}, err
		}

		if err := c.reconcileRecipeOperatorSubscription(); err != nil {
			c.log.Error(err, "unable to reconcile Recipe Operator subscription")
			return ctrl.Result{}, err
		}

		if c.shouldAutoApproveInstallPlans() {
			if err := c.reconcileInstallPlans(); err != nil {
				c.log.Error(err, "unable to reconcile InstallPlans")
				return ctrl.Result{}, err
			}
		}

		if err := c.ensureConsolePlugin(); err != nil {
			c.log.Error(err, "unable to deploy client console")
			return ctrl.Result{}, err
		}

		if err := c.reconcileDelegatedCSI(storageClients); err != nil {
			return ctrl.Result{}, err
		}

		prometheusRule := &monitoringv1.PrometheusRule{}
		if err := k8sYAML.NewYAMLOrJSONDecoder(bytes.NewBufferString(string(pvcPrometheusRules)), 1000).Decode(prometheusRule); err != nil {
			c.log.Error(err, "Unable to retrieve prometheus rules.", "prometheusRule", klog.KRef(prometheusRule.Namespace, prometheusRule.Name))
			return ctrl.Result{}, err
		}

		prometheusRule.SetNamespace(c.OperatorNamespace)

		err = c.createOrUpdate(prometheusRule, func() error {
			applyLabels(c.operatorConfigMap.Data["OCS_METRICS_LABELS"], &prometheusRule.ObjectMeta)
			return c.own(prometheusRule)
		})
		if err != nil {
			c.log.Error(err, "failed to create/update prometheus rules")
			return ctrl.Result{}, err
		}

		c.log.Info("prometheus rules deployed", "prometheusRule", klog.KRef(prometheusRule.Namespace, prometheusRule.Name))
	} else {
		// deletion phase
		if err := c.deletionPhase(); err != nil {
			return ctrl.Result{}, err
		}

		//remove finalizer
		if controllerutil.RemoveFinalizer(c.operatorConfigMap, operatorConfigMapFinalizer) {
			if err := c.Client.Update(c.ctx, c.operatorConfigMap); err != nil {
				return ctrl.Result{}, err
			}
			c.log.Info("finallizer removed successfully")
		}
	}
	return ctrl.Result{}, nil
}

func (c *OperatorConfigMapReconciler) shouldAutoApproveInstallPlans() bool {
	valueAsString, exist := c.operatorConfigMap.Data[disableInstallPlanAutoApprovalKey]
	if !exist {
		return true
	}

	disableInstallPlanAutoApproval, err := strconv.ParseBool(valueAsString)
	if err != nil {
		c.log.Error(err, "failed to parse configmap key data", "key", disableInstallPlanAutoApprovalKey)
		return true
	}

	return !disableInstallPlanAutoApproval
}

func (c *OperatorConfigMapReconciler) reconcileInstallPlans() error {
	approvePatch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"approved":true}}`))
	installPlans := &opv1a1.InstallPlanList{}
	if err := c.list(installPlans, client.InNamespace(c.OperatorNamespace)); err != nil {
		return err
	}
	for idx := range installPlans.Items {
		installPlan := &installPlans.Items[idx]
		if !installPlan.Spec.Approved {
			if err := c.Patch(c.ctx, installPlan, approvePatch); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *OperatorConfigMapReconciler) errorOnRookOwnedCsi() error {
	rookManagedCsiObjects := []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{
			gvk:  appsv1.SchemeGroupVersion.WithKind("Deployment"),
			name: "csi-rbdplugin-provisioner",
		},
		{
			gvk:  appsv1.SchemeGroupVersion.WithKind("Deployment"),
			name: "csi-cephfsplugin-provisioner",
		},
		{
			gvk:  appsv1.SchemeGroupVersion.WithKind("Deployment"),
			name: "csi-nfsplugin-provisioner",
		},
		{
			gvk:  appsv1.SchemeGroupVersion.WithKind("DaemonSet"),
			name: "csi-rbdplugin",
		},
		{
			gvk:  appsv1.SchemeGroupVersion.WithKind("DaemonSet"),
			name: "csi-cephfsplugin",
		},
		{
			gvk:  appsv1.SchemeGroupVersion.WithKind("DaemonSet"),
			name: "csi-nfsplugin",
		},
	}
	for i := range rookManagedCsiObjects {
		entry := &rookManagedCsiObjects[i]
		partialObject := &metav1.PartialObjectMetadata{}
		partialObject.SetGroupVersionKind(entry.gvk)
		partialObject.SetName(entry.name)
		partialObject.SetNamespace(c.OperatorNamespace)
		if err := c.get(partialObject); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to verify presence of %s: %v", entry.name, err)
		} else if err == nil {
			return fmt.Errorf("found presence of %s", entry.name)
		}
	}
	return nil
}

func (c *OperatorConfigMapReconciler) reconcileDelegatedCSI(storageClients *v1alpha1.StorageClientList) error {
	// Wait for CSI deployed by rook to be absent as not to race for resources at node level
	// NOTE: in next minor version this should be removed
	if err := c.errorOnRookOwnedCsi(); err != nil {
		return fmt.Errorf("waiting for rook owned CSI to be removed: %v", err)
	}

	// scc
	scc := &secv1.SecurityContextConstraints{}
	scc.Name = templates.SCCName
	if err := c.createOrUpdate(scc, func() error {
		templates.SetSecurityContextConstraintsDesiredState(scc, c.OperatorNamespace)
		return nil
	}); err != nil {
		return fmt.Errorf("failed to reconcile scc: %v", err)
	}

	// cluster version
	clusterVersion := &configv1.ClusterVersion{}
	clusterVersion.Name = clusterVersionName
	if err := c.get(clusterVersion); err != nil {
		return fmt.Errorf("failed to get cluster version: %v", err)
	}

	historyRecord := utils.Find(clusterVersion.Status.History, func(record *configv1.UpdateHistory) bool {
		return record.State == configv1.CompletedUpdate
	})
	if historyRecord == nil {
		return fmt.Errorf("unable to find the updated cluster version")
	}

	cniNetworkAnnotationValue := ""
	topologyDomainLablesSet := map[string]struct{}{}
	for i := range storageClients.Items {
		storageClient := &storageClients.Items[i]
		annotations := storageClient.GetAnnotations()
		if annotationValue := annotations[cniNetworksAnnotationKey]; annotationValue != "" {
			if cniNetworkAnnotationValue != "" {
				return fmt.Errorf("only one client with CNI network annotation value is supported")
			}
			cniNetworkAnnotationValue = annotationValue
		}

		// merge topology keys from all storage clients,
		// as multiple storage clients hubs can have different topology keys
		if annotationValue := annotations[utils.TopologyDomainLabelsAnnotationKey]; annotationValue != "" {
			// if the annotation value is not empty, it should be a comma separated list of labels
			// e.g. "region,zone"
			for _, label := range strings.Split(annotationValue, ",") {
				topologyDomainLablesSet[label] = struct{}{}
			}
		}
	}

	// csi operator config
	cmName, err := c.getImageSetConfigMapName(historyRecord.Version)
	if err != nil {
		return fmt.Errorf("failed to get desired imageset configmap name: %v", err)
	}
	csiOperatorConfig := &csiopv1a1.OperatorConfig{}
	csiOperatorConfig.Name = templates.CSIOperatorConfigName
	csiOperatorConfig.Namespace = c.OperatorNamespace
	if err := c.createOrUpdate(csiOperatorConfig, func() error {
		if err := c.own(csiOperatorConfig); err != nil {
			return fmt.Errorf("failed to own csi operator config: %v", err)
		}
		templates.CSIOperatorConfigSpec.DeepCopyInto(&csiOperatorConfig.Spec)
		driverSpecDefaults := csiOperatorConfig.Spec.DriverSpecDefaults
		driverSpecDefaults.ImageSet = &corev1.LocalObjectReference{Name: cmName}
		driverSpecDefaults.ClusterName = ptr.To(string(clusterVersion.Spec.ClusterID))
		if c.AvailableCrds[VolumeGroupSnapshotClassCrdName] {
			driverSpecDefaults.SnapshotPolicy = csiopv1a1.VolumeGroupSnapshotPolicy
		}
		csiCtrlPluginHostNetwork, _ := strconv.ParseBool(c.operatorConfigMap.Data[useHostNetworkForCsiControllersKey])
		driverSpecDefaults.ControllerPlugin.HostNetwork = ptr.To(csiCtrlPluginHostNetwork)
		if cniNetworkAnnotationValue != "" {
			if driverSpecDefaults.ControllerPlugin.Annotations == nil {
				driverSpecDefaults.ControllerPlugin.Annotations = map[string]string{}
			}
			driverSpecDefaults.ControllerPlugin.Annotations[cniNetworksAnnotationKey] = cniNetworkAnnotationValue
		}
		if len(topologyDomainLablesSet) > 0 {
			driverSpecDefaults.NodePlugin.Topology = &csiopv1a1.TopologySpec{
				DomainLabels: slices.Collect(maps.Keys(topologyDomainLablesSet)),
			}
		}
		driverSpecDefaults.GenerateOMapInfo = ptr.To(c.shouldGenerateRBDOmapInfo())
		return nil
	}); err != nil {
		return fmt.Errorf("failed to reconcile csi operator config: %v", err)
	}

	// get the custom csi Tolerations
	// TODO: Remove it in 4.20 release
	customNodePluginTolerations, customControllerPluginTolerations, err := c.getCsiCustomTolerations()
	if err != nil {
		return fmt.Errorf("failed to get custom tolerations for csi: %v", err)
	}

	// ceph rbd driver config
	rbdDriver := &csiopv1a1.Driver{}
	rbdDriver.Name = templates.RBDDriverName
	rbdDriver.Namespace = c.OperatorNamespace
	if err := c.createOrUpdate(rbdDriver, func() error {
		if err := c.own(rbdDriver); err != nil {
			return fmt.Errorf("failed to own csi rbd driver: %v", err)
		}
		// only update during initial creation
		if rbdDriver.UID == "" {
			if len(customNodePluginTolerations) > 0 {
				rbdDriver.Spec.NodePlugin = &csiopv1a1.NodePluginSpec{
					PodCommonSpec: csiopv1a1.PodCommonSpec{
						Tolerations: customNodePluginTolerations,
					},
				}
			}
			if len(customControllerPluginTolerations) > 0 {
				rbdDriver.Spec.ControllerPlugin = &csiopv1a1.ControllerPluginSpec{
					PodCommonSpec: csiopv1a1.PodCommonSpec{
						Tolerations: customControllerPluginTolerations,
					},
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to reconcile rbd driver: %v", err)
	}

	// ceph fs driver config
	cephFsDriver := &csiopv1a1.Driver{}
	cephFsDriver.Name = templates.CephFsDriverName
	cephFsDriver.Namespace = c.OperatorNamespace
	if err := c.createOrUpdate(cephFsDriver, func() error {
		if err := c.own(cephFsDriver); err != nil {
			return fmt.Errorf("failed to own csi cephfs driver: %v", err)
		}
		// only update during initial creation
		if cephFsDriver.UID == "" {
			if len(customNodePluginTolerations) > 0 {
				cephFsDriver.Spec.NodePlugin = &csiopv1a1.NodePluginSpec{
					PodCommonSpec: csiopv1a1.PodCommonSpec{
						Tolerations: customNodePluginTolerations,
					},
				}
			}
			if len(customControllerPluginTolerations) > 0 {
				cephFsDriver.Spec.ControllerPlugin = &csiopv1a1.ControllerPluginSpec{
					PodCommonSpec: csiopv1a1.PodCommonSpec{
						Tolerations: customControllerPluginTolerations,
					},
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to reconcile cephfs driver: %v", err)
	}

	// nfs driver config
	nfsDriver := &csiopv1a1.Driver{}
	nfsDriver.Name = templates.NfsDriverName
	nfsDriver.Namespace = c.OperatorNamespace
	if err := c.createOrUpdate(nfsDriver, func() error {
		if err := c.own(nfsDriver); err != nil {
			return fmt.Errorf("failed to own csi nfs driver: %v", err)
		}
		// only update during initial creation
		if nfsDriver.UID == "" {
			if len(customNodePluginTolerations) > 0 {
				nfsDriver.Spec.NodePlugin = &csiopv1a1.NodePluginSpec{
					PodCommonSpec: csiopv1a1.PodCommonSpec{
						Tolerations: customNodePluginTolerations,
					},
				}
			}
			if len(customControllerPluginTolerations) > 0 {
				nfsDriver.Spec.ControllerPlugin = &csiopv1a1.ControllerPluginSpec{
					PodCommonSpec: csiopv1a1.PodCommonSpec{
						Tolerations: customControllerPluginTolerations,
					},
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to reconcile nfs driver: %v", err)
	}

	return nil
}

func (c *OperatorConfigMapReconciler) deletionPhase() error {
	clientsList := &v1alpha1.StorageClientList{}
	if err := c.list(clientsList, client.Limit(1)); err != nil {
		c.log.Error(err, "unable to verify StorageClients presence prior to removal of CSI resources")
		return err
	} else if len(clientsList.Items) != 0 {
		err = fmt.Errorf("failed to clean up resources: storage clients are present on the cluster")
		c.log.Error(err, "Waiting for all storageClients to be deleted.")
		return err
	}

	if err := c.deleteDelegatedCSI(); err != nil {
		return err
	}

	whConfig := &admrv1.ValidatingWebhookConfiguration{}
	whConfig.Name = templates.SubscriptionWebhookName
	if err := c.delete(whConfig); err != nil {
		c.log.Error(err, "failed to delete subscription webhook")
		return err
	}

	return nil
}

func (c *OperatorConfigMapReconciler) createOrUpdate(obj client.Object, f controllerutil.MutateFn) error {
	result, err := controllerutil.CreateOrUpdate(c.ctx, c.Client, obj, f)
	if err != nil {
		return err
	}
	c.log.Info("successfully created or updated", "operation", result, "name", obj.GetName())
	return nil
}

func (c *OperatorConfigMapReconciler) own(obj client.Object) error {
	return controllerutil.SetControllerReference(c.operatorConfigMap, obj, c.Client.Scheme())
}

// applyLabels adds labels to object meta, overwriting keys that are already defined.
func applyLabels(label string, t *metav1.ObjectMeta) {
	// Create a map to store the configuration
	promLabel := make(map[string]string)

	labels := strings.Split(label, "\n")
	// Loop through the lines and extract key-value pairs
	for _, line := range labels {
		if len(line) == 0 {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		promLabel[key] = value
	}

	t.Labels = promLabel
}

func (c *OperatorConfigMapReconciler) ensureConsolePlugin() error {
	c.consoleDeployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      console.DeploymentName,
			Namespace: c.OperatorNamespace,
		},
	}

	err := c.get(c.consoleDeployment)
	if err != nil {
		c.log.Error(err, "failed to get the deployment for the console")
		return err
	}

	nginxConf := console.GetNginxConf()
	nginxConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      console.NginxConfigMapName,
			Namespace: c.OperatorNamespace,
		},
		Data: map[string]string{
			"nginx.conf": nginxConf,
		},
	}
	err = c.createOrUpdate(nginxConfigMap, func() error {
		if consoleConfigMapData := nginxConfigMap.Data["nginx.conf"]; consoleConfigMapData != nginxConf {
			nginxConfigMap.Data["nginx.conf"] = nginxConf
		}
		return controllerutil.SetControllerReference(c.consoleDeployment, nginxConfigMap, c.Scheme)
	})

	if err != nil {
		c.log.Error(err, "failed to create nginx config map")
		return err
	}

	consoleService := console.GetService(c.ConsolePort, c.OperatorNamespace)

	err = c.createOrUpdate(consoleService, func() error {
		if err := controllerutil.SetControllerReference(c.consoleDeployment, consoleService, c.Scheme); err != nil {
			return err
		}
		console.GetService(c.ConsolePort, c.OperatorNamespace).DeepCopyInto(consoleService)
		return nil
	})

	if err != nil {
		c.log.Error(err, "failed to create/update service for console")
		return err
	}

	consolePlugin := console.GetConsolePlugin(c.ConsolePort, c.OperatorNamespace)
	err = c.createOrUpdate(consolePlugin, func() error {
		// preserve the resourceVersion of the consolePlugin
		resourceVersion := consolePlugin.ResourceVersion
		console.GetConsolePlugin(c.ConsolePort, c.OperatorNamespace).DeepCopyInto(consolePlugin)
		consolePlugin.ResourceVersion = resourceVersion
		return nil
	})

	if err != nil {
		c.log.Error(err, "failed to create/update consoleplugin")
		return err
	}

	return nil
}

func (c *OperatorConfigMapReconciler) getNoobaaSubManagementConfig() bool {
	valAsString, ok := c.operatorConfigMap.Data[manageNoobaaSubKey]
	if !ok {
		return true
	}
	val, err := strconv.ParseBool(valAsString)
	if err != nil {
		c.log.Error(
			err,
			"Unsupported value under manageNoobaaSubscription key",
		)
		return true
	}
	return val
}

func (c *OperatorConfigMapReconciler) shouldGenerateRBDOmapInfo() bool {
	valAsString := strings.ToLower(c.operatorConfigMap.Data[generateRbdOMapInfoKey])
	return valAsString == strconv.FormatBool(true)
}

func (c *OperatorConfigMapReconciler) get(obj client.Object, opts ...client.GetOption) error {
	return c.Get(c.ctx, client.ObjectKeyFromObject(obj), obj, opts...)
}

func (c *OperatorConfigMapReconciler) reconcileSubscriptionValidatingWebhook() error {
	whConfig := &admrv1.ValidatingWebhookConfiguration{}
	whConfig.Name = templates.SubscriptionWebhookName

	// TODO (lgangava): after change to configmap controller, need to remove webhook during deletion
	err := c.createOrUpdate(whConfig, func() error {

		// openshift fills in the ca on finding this annotation
		whConfig.Annotations = map[string]string{
			"service.beta.openshift.io/inject-cabundle": "true",
		}

		var caBundle []byte
		if len(whConfig.Webhooks) == 0 {
			whConfig.Webhooks = make([]admrv1.ValidatingWebhook, 1)
		} else {
			// do not mutate CA bundle that was injected by openshift
			caBundle = whConfig.Webhooks[0].ClientConfig.CABundle
		}

		// webhook desired state
		var wh *admrv1.ValidatingWebhook = &whConfig.Webhooks[0]
		templates.SubscriptionValidatingWebhook.DeepCopyInto(wh)

		wh.Name = whConfig.Name
		// only send requests received from own namespace
		wh.NamespaceSelector = &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"kubernetes.io/metadata.name": c.OperatorNamespace,
			},
		}
		// only send resources matching the label
		wh.ObjectSelector = &metav1.LabelSelector{
			MatchLabels: map[string]string{
				subscriptionLabelKey: subscriptionLabelValue,
			},
		}
		// preserve the existing (injected) CA bundle if any
		wh.ClientConfig.CABundle = caBundle
		// send request to the service running in own namespace
		wh.ClientConfig.Service.Namespace = c.OperatorNamespace

		return nil
	})

	if err != nil {
		return err
	}

	c.log.Info("successfully registered validating webhook")
	return nil
}

func (c *OperatorConfigMapReconciler) reconcileClientOperatorSubscription() error {

	clientSubscription, err := c.getSubscriptionByPackageName("ocs-client-operator")
	if err != nil {
		return err
	}

	updateRequired := utils.AddLabel(clientSubscription, subscriptionLabelKey, subscriptionLabelValue)
	if c.subscriptionChannel != "" && c.subscriptionChannel != clientSubscription.Spec.Channel {
		clientSubscription.Spec.Channel = c.subscriptionChannel
		// TODO: https://github.com/red-hat-storage/ocs-client-operator/issues/130
		// there can be a possibility that platform is behind, even then updating the channel will only make subscription to be in upgrading state
		// without any side effects for already running workloads. However, this will be a silent failure and need to be fixed via above TODO issue.
		updateRequired = true
	}

	if updateRequired {
		if err := c.update(clientSubscription); err != nil {
			return fmt.Errorf("failed to update subscription channel to %v: %v", c.subscriptionChannel, err)
		}
	}
	return nil
}

func (c *OperatorConfigMapReconciler) reconcileNoobaaOperatorSubscription() error {
	noobaaSubscription, err := c.getSubscriptionByPackageName("mcg-operator")
	if kerrors.IsNotFound(err) {
		noobaaSubscription, err = c.getSubscriptionByPackageName("noobaa-operator")
	}
	if err != nil {
		return err
	}
	if c.subscriptionChannel != "" && c.subscriptionChannel != noobaaSubscription.Spec.Channel {
		noobaaSubscription.Spec.Channel = c.subscriptionChannel
		if err := c.update(noobaaSubscription); err != nil {
			return fmt.Errorf("failed to update subscription channel of 'mcg-operator' to %v: %v", c.subscriptionChannel, err)
		}
	}
	return nil
}

func (c *OperatorConfigMapReconciler) reconcileCSIAddonsOperatorSubscription() error {
	addonsSubscription, err := c.getSubscriptionByPackageName("odf-csi-addons-operator")
	if kerrors.IsNotFound(err) {
		addonsSubscription, err = c.getSubscriptionByPackageName("csi-addons")
	}
	if err != nil {
		return err
	}
	if c.subscriptionChannel != "" && c.subscriptionChannel != addonsSubscription.Spec.Channel {
		addonsSubscription.Spec.Channel = c.subscriptionChannel
		if err := c.update(addonsSubscription); err != nil {
			return fmt.Errorf("failed to update subscription channel of 'odf-csi-addons-operator' to %v: %v", c.subscriptionChannel, err)
		}
	}
	return nil
}

func (c *OperatorConfigMapReconciler) reconcileCephCSIOperatorSubscription() error {
	cephCsiOperatorSubscription, err := c.getSubscriptionByPackageName("cephcsi-operator")
	if kerrors.IsNotFound(err) {
		cephCsiOperatorSubscription, err = c.getSubscriptionByPackageName("ceph-csi-operator")
	}
	if err != nil {
		return err
	}
	if c.subscriptionChannel != "" && c.subscriptionChannel != cephCsiOperatorSubscription.Spec.Channel {
		cephCsiOperatorSubscription.Spec.Channel = c.subscriptionChannel
		if err := c.update(cephCsiOperatorSubscription); err != nil {
			return fmt.Errorf("failed to update subscription channel of 'cephcsi-operator' to %v: %v", c.subscriptionChannel, err)
		}
	}
	return nil
}

func (c *OperatorConfigMapReconciler) reconcileRecipeOperatorSubscription() error {
	recipeOperatorSubscription, err := c.getSubscriptionByPackageName("recipe")
	if err != nil {
		return err
	}
	if c.subscriptionChannel != "" && c.subscriptionChannel != recipeOperatorSubscription.Spec.Channel {
		recipeOperatorSubscription.Spec.Channel = c.subscriptionChannel
		if err := c.update(recipeOperatorSubscription); err != nil {
			return fmt.Errorf("failed to update subscription channel of 'recipe' to %v: %v", c.subscriptionChannel, err)
		}
	}
	return nil
}

func (c *OperatorConfigMapReconciler) reconcileWebhookService() error {
	svc := &corev1.Service{}
	svc.Name = templates.WebhookServiceName
	svc.Namespace = c.OperatorNamespace
	err := c.createOrUpdate(svc, func() error {
		if err := c.own(svc); err != nil {
			return err
		}
		utils.AddAnnotation(svc, "service.beta.openshift.io/serving-cert-secret-name", "webhook-cert-secret")
		templates.WebhookService.Spec.DeepCopyInto(&svc.Spec)
		return nil
	})
	if err != nil {
		return err
	}
	c.log.Info("successfully reconcile webhook service")
	return nil
}

func (c *OperatorConfigMapReconciler) list(obj client.ObjectList, opts ...client.ListOption) error {
	return c.List(c.ctx, obj, opts...)
}

func (c *OperatorConfigMapReconciler) update(obj client.Object, opts ...client.UpdateOption) error {
	return c.Update(c.ctx, obj, opts...)
}

func (c *OperatorConfigMapReconciler) delete(obj client.Object, opts ...client.DeleteOption) error {
	if err := c.Delete(c.ctx, obj, opts...); err != nil && !kerrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (c *OperatorConfigMapReconciler) getSubscriptionByPackageName(pkgName string) (*opv1a1.Subscription, error) {
	subList := &opv1a1.SubscriptionList{}
	if err := c.list(
		subList,
		client.MatchingFields{subPackageIndexName: pkgName},
		client.InNamespace(c.OperatorNamespace),
		client.Limit(1),
	); err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %v", err)
	}

	if len(subList.Items) == 0 {
		return nil, kerrors.NewNotFound(opv1a1.Resource("subscriptions"), pkgName)
	} else if len(subList.Items) > 1 {
		return nil, fmt.Errorf("more than one subscription found for %v", pkgName)
	}

	return &subList.Items[0], nil
}

func (c *OperatorConfigMapReconciler) getDesiredSubscriptionChannel(storageClients *v1alpha1.StorageClientList) (string, error) {

	var desiredChannel string
	for idx := range storageClients.Items {
		// empty if annotation doesn't exist or else gets desired channel
		channel := storageClients.
			Items[idx].
			GetAnnotations()[utils.DesiredSubscriptionChannelAnnotationKey]
		// skip clients with no/empty desired channel annotation
		if channel != "" {
			// check if we already established a desired channel
			if desiredChannel == "" {
				desiredChannel = channel
			}
			// check for agreement between clients
			if channel != desiredChannel {
				desiredChannel = ""
				// two clients didn't agree for a same channel and no need to continue further
				break
			}
		}
	}
	return desiredChannel, nil
}

func (c *OperatorConfigMapReconciler) getImageSetConfigMapName(clusterVersion string) (string, error) {
	configMaps := &corev1.ConfigMapList{}
	if err := c.list(configMaps, client.InNamespace(c.OperatorNamespace), client.HasLabels{csiImagesConfigMapLabel}); err != nil {
		return "", err
	}
	platformVersion := version.MustParseGeneric(clusterVersion)
	closestMinor := int64(-1)
	var configMapName string
	for idx := range configMaps.Items {
		cm := &configMaps.Items[idx]
		imageVersion := version.MustParseGeneric(cm.GetLabels()[csiImagesConfigMapLabel])
		c.log.Info("searching for the most compatible CSI image version", "CSI", imageVersion, "Platform", platformVersion)

		// only check image versions that are not higher than platform
		if imageVersion.Major() == platformVersion.Major() && imageVersion.Minor() <= platformVersion.Minor() {
			// filter the images closes to platform version
			if int64(imageVersion.Minor()) > closestMinor {
				configMapName = cm.Name
				closestMinor = int64(imageVersion.Minor())
			}
			// exact match and early exit
			if closestMinor == int64(platformVersion.Minor()) {
				break
			}
		} else {
			c.log.Info("skipping imagesets with version greater than platform version")
		}
	}

	if configMapName == "" {
		return "", fmt.Errorf("failed to find configmap containing images suitable for %v platform version", platformVersion)
	}
	return configMapName, nil
}

func (c *OperatorConfigMapReconciler) deleteDelegatedCSI() error {
	// NOTE: csi operator config and driver CRs are garbage collected via ownerref, so we need to remove only SCC
	scc := &secv1.SecurityContextConstraints{}
	scc.Name = templates.SCCName
	if err := c.delete(scc); err != nil {
		return err
	}
	return nil
}

// TODO: Remove it in 4.20 release
func (c *OperatorConfigMapReconciler) getCsiCustomTolerations() ([]corev1.Toleration, []corev1.Toleration, error) {
	const (
		rookCephOperatorConfigName  = "rook-ceph-operator-config"
		csiPluginTolerationKey      = "CSI_PLUGIN_TOLERATIONS"
		csiProvisionerTolerationKey = "CSI_PROVISIONER_TOLERATIONS"
	)
	var nodePluginTolerations, controllerPluginTolerations []corev1.Toleration

	// list the rook ceph cm
	rookCephOperatorConfig := &corev1.ConfigMap{}
	rookCephOperatorConfig.Name = rookCephOperatorConfigName
	rookCephOperatorConfig.Namespace = c.OperatorNamespace

	if err := c.get(rookCephOperatorConfig); kerrors.IsNotFound(err) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, fmt.Errorf("failed to get Rook Ceph Operator ConfigMap: %v", err)
	}

	// Get the CSI tolerations from the Rook Ceph Operator ConfigMap
	csiPluginTolerations := rookCephOperatorConfig.Data[csiPluginTolerationKey]
	if csiPluginTolerations != "" {
		var err error
		if nodePluginTolerations, err = parseTolerations(csiPluginTolerations); err != nil {
			return nil, nil, fmt.Errorf("failed to parse csi plugin tolerations: %v", err)
		}
	}

	csiProvisionerToleration := rookCephOperatorConfig.Data[csiProvisionerTolerationKey]
	if csiProvisionerToleration != "" {
		var err error
		if controllerPluginTolerations, err = parseTolerations(csiProvisionerToleration); err != nil {
			return nil, nil, fmt.Errorf("failed to parse csi controllerPlugin tolerations: %v", err)
		}
	}

	return nodePluginTolerations, controllerPluginTolerations, nil
}

func parseTolerations(csiPluginTolerations string) ([]corev1.Toleration, error) {
	if csiPluginTolerations == "" {
		return nil, nil
	}

	rawJSON, err := yaml.ToJSON([]byte(csiPluginTolerations))
	if err != nil {
		return nil, err
	}

	var tolerations []corev1.Toleration
	err = json.Unmarshal(rawJSON, &tolerations)
	if err != nil {
		return nil, err
	}

	for i := range tolerations {
		if tolerations[i].Key == "" {
			tolerations[i].Operator = corev1.TolerationOpExists
		}

		if tolerations[i].Operator == corev1.TolerationOpExists {
			tolerations[i].Value = ""
		}
	}
	return tolerations, nil
}

func (c *OperatorConfigMapReconciler) removeNoobaa() error {
	noobaa := &nbv1.NooBaa{}
	noobaa.Name = noobaaCrName
	noobaa.Namespace = c.OperatorNamespace

	if err := c.get(noobaa); !meta.IsNoMatchError(err) && client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to get remote noobaa: %v", err)
	} else if noobaa.UID != "" && noobaa.GetDeletionTimestamp().IsZero() {
		index := slices.IndexFunc(
			noobaa.GetOwnerReferences(),
			func(ref metav1.OwnerReference) bool {
				return ref.Kind == "StorageClient"
			},
		)
		if index != -1 {
			noobaa.Spec.CleanupPolicy.AllowNoobaaDeletion = true
			if err := c.update(noobaa); err != nil {
				return fmt.Errorf("failed to update noobaa %v: %v", noobaa.Name, err)
			}
			if err := c.delete(noobaa); err != nil {
				return fmt.Errorf("failed to delete remote noobaa: %v", err)
			}
		}
	}
	return nil
}

func (c *OperatorConfigMapReconciler) removeNoobaaOperator() error {

	nb := &metav1.PartialObjectMetadataList{}
	nb.SetGroupVersionKind(nbv1.SchemeGroupVersion.WithKind("NooBaa"))
	if err := c.list(nb, client.Limit(1)); err != nil && !meta.IsNoMatchError(err) {
		return fmt.Errorf("failed to list noobaa: %v", err)
	}
	if len(nb.Items) != 0 {
		return nil
	}

	noobaaSubscription, err := c.getSubscriptionByPackageName("mcg-operator")
	if client.IgnoreNotFound(err) != nil {
		return err
	} else if noobaaSubscription == nil {
		return nil
	}
	index := slices.IndexFunc(
		noobaaSubscription.OwnerReferences,
		func(ref metav1.OwnerReference) bool {
			return ref.Kind == "Subscription" && ref.Name == "odf-operator"
		},
	)
	if index != -1 {
		return nil
	}

	csv := &opv1a1.ClusterServiceVersion{}
	csv.Name = noobaaSubscription.Status.InstalledCSV
	csv.Namespace = c.OperatorNamespace
	if csv.Name != "" {
		if err := c.get(csv); client.IgnoreNotFound(err) != nil {
			c.log.Error(err, "failed to get noobaa operator csv")
			return err
		} else if csv.UID != "" && csv.GetDeletionTimestamp().IsZero() {
			if err := c.delete(csv); err != nil {
				c.log.Error(err, "failed to delete noobaa operator csv")
				return err
			}
		}
	}
	if noobaaSubscription.GetDeletionTimestamp().IsZero() {
		if err = c.delete(noobaaSubscription); err != nil {
			return err
		}
	}
	return nil
}
