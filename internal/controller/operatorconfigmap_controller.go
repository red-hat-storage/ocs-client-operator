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
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	// The embed package is required for the prometheus rule files
	_ "embed"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/console"
	"github.com/red-hat-storage/ocs-client-operator/pkg/csi"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	secv1 "github.com/openshift/api/security/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	admrv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/version"
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
	operatorConfigMapName   = "ocs-client-operator-config"
	csiSidecarConfigMapName = "ceph-csi-sidecar-config"

	csiOMAPGeneratorKey = "CSI_ENABLE_OMAP_GENERATOR"

	// ClusterVersionName is the name of the ClusterVersion object in the
	// openshift cluster.
	clusterVersionName     = "version"
	deployCSIKey           = "DEPLOY_CSI"
	manageNoobaaSubKey     = "manageNoobaaSubscription"
	subscriptionLabelKey   = "managed-by"
	subscriptionLabelValue = "webhook.subscription.ocs.openshift.io"

	operatorConfigMapFinalizer = "ocs-client-operator.ocs.openshift.io/storageused"
	subPackageIndexName        = "index:subscriptionPackage"
	csiImagesConfigMapLabel    = "ocs.openshift.io/csi-images-version"
)

// OperatorConfigMapReconciler reconciles a ClusterVersion object
type OperatorConfigMapReconciler struct {
	client.Client
	OperatorNamespace string
	ConsolePort       int32
	Scheme            *runtime.Scheme

	log                 logr.Logger
	ctx                 context.Context
	operatorConfigMap   *corev1.ConfigMap
	consoleDeployment   *appsv1.Deployment
	cephFSDeployment    *appsv1.Deployment
	cephFSDaemonSet     *appsv1.DaemonSet
	rbdDeployment       *appsv1.Deployment
	rbdDaemonSet        *appsv1.DaemonSet
	scc                 *secv1.SecurityContextConstraints
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
				return namespace == c.OperatorNamespace && (name == operatorConfigMapName || name == csiSidecarConfigMapName)
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

	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, configMapPredicates).
		Owns(&corev1.Service{}, servicePredicate).
		Watches(&configv1.ClusterVersion{}, enqueueConfigMapRequest, clusterVersionPredicates).
		Watches(&extv1.CustomResourceDefinition{}, enqueueConfigMapRequest, builder.OnlyMetadata).
		Watches(&opv1a1.Subscription{}, enqueueConfigMapRequest, subscriptionPredicates).
		Watches(&admrv1.ValidatingWebhookConfiguration{}, enqueueConfigMapRequest, webhookPredicates).
		Watches(&v1alpha1.StorageClient{}, enqueueConfigMapRequest, builder.WithPredicates(predicate.AnnotationChangedPredicate{}))

	generationChangePredicate := predicate.GenerationChangedPredicate{}
	if utils.DelegateCSI {
		bldr = bldr.
			Owns(&csiopv1a1.OperatorConfig{}, builder.WithPredicates(generationChangePredicate)).
			Owns(&csiopv1a1.Driver{}, builder.WithPredicates(generationChangePredicate))
	}

	return bldr.Complete(c)
}

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch
//+kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=deployments/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=daemonsets/finalizers,verbs=update
//+kubebuilder:rbac:groups="storage.k8s.io",resources=csidrivers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=configmaps/finalizers,verbs=update
//+kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=get;list;watch;create;patch;update;delete
//+kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list;watch;create;update
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=console.openshift.io,resources=consoleplugins,verbs=*
//+kubebuilder:rbac:groups=operators.coreos.com,resources=subscriptions,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclusters,verbs=get;list
//+kubebuilder:rbac:groups=csi.ceph.io,resources=operatorconfigs,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=csi.ceph.io,resources=drivers,verbs=get;list;update;create;watch;delete

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (c *OperatorConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c.ctx = ctx
	c.log = log.FromContext(ctx, "OperatorConfigMap", req)
	c.log.Info("Reconciling OperatorConfigMap")

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

	var err error
	if c.subscriptionChannel, err = c.getDesiredSubscriptionChannel(); err != nil {
		return reconcile.Result{}, err
	}

	if c.operatorConfigMap.GetDeletionTimestamp().IsZero() {

		//ensure finalizer
		if controllerutil.AddFinalizer(c.operatorConfigMap, operatorConfigMapFinalizer) {
			c.log.Info("finalizer missing on the operatorConfigMap resource, adding...")
			if err := c.Client.Update(c.ctx, c.operatorConfigMap); err != nil {
				return ctrl.Result{}, err
			}
		}

		if err := c.reconcileWebhookService(); err != nil {
			c.log.Error(err, "unable to reconcile webhook service")
			return ctrl.Result{}, err
		}

		if err := c.reconcileSubscriptionValidatingWebhook(); err != nil {
			c.log.Error(err, "unable to register subscription validating webhook")
			return ctrl.Result{}, err
		}

		if err := c.reconcileClientOperatorSubscription(); err != nil {
			c.log.Error(err, "unable to reconcile client operator subscription")
			return ctrl.Result{}, err
		}

		//only reconcile noobaa-operator for remote clusters
		if c.getNoobaaSubManagementConfig() {
			if err := c.reconcileNoobaaOperatorSubscription(); err != nil {
				c.log.Error(err, "unable to reconcile Noobaa Operator subscription")
				return ctrl.Result{}, err
			}
		}

		if err := c.reconcileCSIAddonsOperatorSubscription(); err != nil {
			c.log.Error(err, "unable to reconcile CSI Addons subscription")
			return ctrl.Result{}, err
		}

		if err := c.ensureConsolePlugin(); err != nil {
			c.log.Error(err, "unable to deploy client console")
			return ctrl.Result{}, err
		}

		if deployCSI, err := c.getDeployCSIConfig(); err != nil {
			c.log.Error(err, "failed to perform precheck for deploying CSI")
			return ctrl.Result{}, err
		} else if deployCSI {

			var err error
			if utils.DelegateCSI {
				err = c.reconcileDelegatedCSI()
			} else {
				err = c.reconcileCSI()
			}

			if err != nil {
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
		}
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

func (c *OperatorConfigMapReconciler) reconcileDelegatedCSI() error {
	// remove older CSI deployments and daemonsets as the resources created by csi-operator is different.
	// we are guaranteed to use kernel mounts removing daemonsts will not pose any risk
	// NOTE: in next minor version this should be removed
	if err := c.deleteOlderCSIResources(); err != nil {
		return fmt.Errorf("failed to remove older csi resources: %v", err)
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

	sideCarConfig := &corev1.ConfigMap{}
	sideCarConfig.Name = csiSidecarConfigMapName
	sideCarConfig.Namespace = c.OperatorNamespace

	err := c.get(sideCarConfig)
	if err != nil && !kerrors.IsNotFound(err) {
		c.log.Error(err, "failed to get csi side car configmap", "name", sideCarConfig)
		return err
	}

	var deployOMAP bool
	if kerrors.IsNotFound(err) {
		if value, ok := sideCarConfig.Data[csiOMAPGeneratorKey]; ok {
			deployOMAP, err = strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("failed to parse value for %q in operator configmap as a boolean: %v", deployCSIKey, err)
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
		csiOperatorConfig.Spec.DriverSpecDefaults.ImageSet = &corev1.LocalObjectReference{Name: cmName}
		csiOperatorConfig.Spec.DriverSpecDefaults.ClusterName = ptr.To(string(clusterVersion.Spec.ClusterID))
		csiOperatorConfig.Spec.DriverSpecDefaults.GenerateOMapInfo = ptr.To(deployOMAP)
		return nil
	}); err != nil {
		return fmt.Errorf("failed to reconcile csi operator config: %v", err)
	}

	// ceph rbd driver config
	rbdDriver := &csiopv1a1.Driver{}
	rbdDriver.Name = templates.RBDDriverName
	rbdDriver.Namespace = c.OperatorNamespace

	// indicate csi-op to own RBD CSIDriver resource
	ownerObjKey := client.ObjectKeyFromObject(rbdDriver)
	bytes, err := json.Marshal(ownerObjKey)
	if err != nil {
		return fmt.Errorf("failed to marshal RBD Driver object key: %v", err)
	}
	rbdCsiDriver := &storagev1.CSIDriver{}
	rbdCsiDriver.Name = templates.RBDDriverName
	if err := c.get(rbdCsiDriver); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to get RBD CSIDriver resource: %v", err)
	} else if rbdCsiDriver.UID != "" && utils.AddAnnotation(rbdCsiDriver, "csi.ceph.io/ownerref", string(bytes)) {
		if err = c.update(rbdCsiDriver); err != nil {
			return fmt.Errorf("failed to transfer ownership of RBD CSIDriver resource: %v", err)
		}
	}

	if err := c.createOrUpdate(rbdDriver, func() error {
		if err := c.own(rbdDriver); err != nil {
			return fmt.Errorf("failed to own csi rbd driver: %v", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to reconcile rbd driver: %v", err)
	}

	// ceph fs driver config
	cephFsDriver := &csiopv1a1.Driver{}
	cephFsDriver.Name = templates.CephFsDriverName
	cephFsDriver.Namespace = c.OperatorNamespace

	// indicate csi-op to own CephFs CSIDriver resource
	ownerObjKey = client.ObjectKeyFromObject(cephFsDriver)
	bytes, err = json.Marshal(ownerObjKey)
	if err != nil {
		return fmt.Errorf("failed to marshal CephFs Driver object key: %v", err)
	}
	cephFsCsiDriver := &storagev1.CSIDriver{}
	cephFsCsiDriver.Name = templates.CephFsDriverName
	if err := c.get(cephFsCsiDriver); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to get CephFs CSIDriver resource: %v", err)
	} else if cephFsCsiDriver.UID != "" && utils.AddAnnotation(cephFsCsiDriver, "csi.ceph.io/ownerref", string(bytes)) {
		if err = c.update(cephFsCsiDriver); err != nil {
			return fmt.Errorf("failed to transfer ownership of CephFs CSIDriver resource: %v", err)
		}
	}

	if err := c.createOrUpdate(cephFsDriver, func() error {
		if err := c.own(cephFsDriver); err != nil {
			return fmt.Errorf("failed to own csi cephfs driver: %v", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to reconcile cephfs driver: %v", err)
	}

	return nil
}

func (c *OperatorConfigMapReconciler) getAndDeleteResource(obj client.Object) error {
	if err := c.get(obj); err == nil {
		if err = c.delete(obj); err != nil {
			return fmt.Errorf("failed to delete %s: %v", client.ObjectKeyFromObject(obj), err)
		}
	} else if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to get get %s: %v", client.ObjectKeyFromObject(obj), err)
	}
	return nil
}

func (c *OperatorConfigMapReconciler) deleteOlderCSIResources() error {
	rbdDeployment := &appsv1.Deployment{}
	rbdDeployment.Name = csi.RBDDeploymentName
	rbdDeployment.Namespace = c.OperatorNamespace
	// doing a get hits cache and reduces round trip to api server when trying
	// to delete non existing resource in every reconcile
	if err := c.getAndDeleteResource(rbdDeployment); err != nil {
		return err
	}

	rbdDaemonSet := &appsv1.DaemonSet{}
	rbdDaemonSet.Name = csi.RBDDaemonSetName
	rbdDaemonSet.Namespace = c.OperatorNamespace
	if err := c.getAndDeleteResource(rbdDaemonSet); err != nil {
		return err
	}

	cephFsDeployment := &appsv1.Deployment{}
	cephFsDeployment.Name = csi.CephFSDeploymentName
	cephFsDeployment.Namespace = c.OperatorNamespace
	if err := c.getAndDeleteResource(cephFsDeployment); err != nil {
		return err
	}

	cephFsDaemonSet := &appsv1.DaemonSet{}
	cephFsDaemonSet.Name = csi.CephFSDaemonSetName
	cephFsDaemonSet.Namespace = c.OperatorNamespace
	if err := c.getAndDeleteResource(cephFsDaemonSet); err != nil {
		return err
	}

	return nil
}

func (c *OperatorConfigMapReconciler) reconcileCSI() error {

	clusterVersion := &configv1.ClusterVersion{}
	clusterVersion.Name = clusterVersionName
	if err := c.get(clusterVersion); err != nil {
		c.log.Error(err, "failed to get the clusterVersion version of the OCP cluster")
		return err
	}

	if err := csi.InitializeSidecars(c.log, clusterVersion.Status.Desired.Version); err != nil {
		c.log.Error(err, "unable to initialize sidecars")
		return err
	}

	c.scc = &secv1.SecurityContextConstraints{
		ObjectMeta: metav1.ObjectMeta{
			Name: csi.SCCName,
		},
	}
	err := c.createOrUpdate(c.scc, func() error {
		// TODO: this is a hack to preserve the resourceVersion of the SCC
		resourceVersion := c.scc.ResourceVersion
		csi.SetSecurityContextConstraintsDesiredState(c.scc, c.OperatorNamespace)
		c.scc.ResourceVersion = resourceVersion
		return nil
	})
	if err != nil {
		c.log.Error(err, "unable to create/update SCC")
		return err
	}

	// create the monitor configmap for the csi drivers but never updates it.
	// This is because the monitor configurations are added to the configmap
	// when user creates storageclaims.
	monConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templates.MonConfigMapName,
			Namespace: c.OperatorNamespace,
		},
		Data: map[string]string{
			"config.json": "[]",
		},
	}
	if err := c.own(monConfigMap); err != nil {
		return err
	}

	if err := c.create(monConfigMap); err != nil && !kerrors.IsAlreadyExists(err) {
		c.log.Error(err, "failed to create monitor configmap", "name", monConfigMap.Name)
		return err
	}

	// create the encryption configmap for the csi driver but never updates it.
	// This is because the encryption configuration are added to the configmap
	// by the users before they create the encryption storageclaims.
	encConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templates.EncryptionConfigMapName,
			Namespace: c.OperatorNamespace,
		},
		Data: map[string]string{
			"config.json": "[]",
		},
	}
	if err := c.own(encConfigMap); err != nil {
		return err
	}

	if err := c.create(encConfigMap); err != nil && !kerrors.IsAlreadyExists(err) {
		c.log.Error(err, "failed to create monitor configmap", "name", encConfigMap.Name)
		return err
	}

	c.cephFSDeployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csi.CephFSDeploymentName,
			Namespace: c.OperatorNamespace,
		},
	}
	err = c.createOrUpdate(c.cephFSDeployment, func() error {
		if err := c.own(c.cephFSDeployment); err != nil {
			return err
		}
		csi.SetCephFSDeploymentDesiredState(c.cephFSDeployment)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update cephfs deployment")
		return err
	}

	c.cephFSDaemonSet = &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csi.CephFSDaemonSetName,
			Namespace: c.OperatorNamespace,
		},
	}
	err = c.createOrUpdate(c.cephFSDaemonSet, func() error {
		if err := c.own(c.cephFSDaemonSet); err != nil {
			return err
		}
		csi.SetCephFSDaemonSetDesiredState(c.cephFSDaemonSet)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update cephfs daemonset")
		return err
	}

	c.rbdDeployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csi.RBDDeploymentName,
			Namespace: c.OperatorNamespace,
		},
	}
	err = c.createOrUpdate(c.rbdDeployment, func() error {
		if err := c.own(c.rbdDeployment); err != nil {
			return err
		}
		csi.SetRBDDeploymentDesiredState(c.rbdDeployment)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update rbd deployment")
		return err
	}

	c.rbdDaemonSet = &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csi.RBDDaemonSetName,
			Namespace: c.OperatorNamespace,
		},
	}
	err = c.createOrUpdate(c.rbdDaemonSet, func() error {
		if err := c.own(c.rbdDaemonSet); err != nil {
			return err
		}
		csi.SetRBDDaemonSetDesiredState(c.rbdDaemonSet)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update rbd daemonset")
		return err
	}

	// Need to handle deletion of the csiDriver object, we cannot set
	// ownerReference on it as its cluster scoped resource
	cephfsCSIDriver := templates.CephFSCSIDriver.DeepCopy()
	cephfsCSIDriver.ObjectMeta.Name = csi.GetCephFSDriverName()
	if err := csi.CreateCSIDriver(c.ctx, c.Client, cephfsCSIDriver); err != nil {
		c.log.Error(err, "unable to create cephfs CSIDriver")
		return err
	}

	rbdCSIDriver := templates.RbdCSIDriver.DeepCopy()
	rbdCSIDriver.ObjectMeta.Name = csi.GetRBDDriverName()
	if err := csi.CreateCSIDriver(c.ctx, c.Client, rbdCSIDriver); err != nil {
		c.log.Error(err, "unable to create rbd CSIDriver")
		return err
	}
	return nil
}

func (c *OperatorConfigMapReconciler) deletionPhase() error {
	claimsList := &v1alpha1.StorageClaimList{}
	if err := c.list(claimsList, client.Limit(1)); err != nil {
		c.log.Error(err, "unable to verify StorageClaims presence prior to removal of CSI resources")
		return err
	} else if len(claimsList.Items) != 0 {
		err = fmt.Errorf("failed to clean up resources: storage claims are present on the cluster")
		c.log.Error(err, "Waiting for all storageClaims to be deleted.")
		return err
	}

	var err error
	if utils.DelegateCSI {
		err = c.deleteDelegatedCSI()
	} else {
		err = c.deleteCSI()
	}
	if err != nil {
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

func (c *OperatorConfigMapReconciler) create(obj client.Object) error {
	return c.Client.Create(c.ctx, obj)
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

func (c *OperatorConfigMapReconciler) getDeployCSIConfig() (bool, error) {
	data := c.operatorConfigMap.Data
	if data == nil {
		data = map[string]string{}
	}

	var deployCSI bool
	var err error
	if value, ok := data[deployCSIKey]; ok {
		deployCSI, err = strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf("failed to parse value for %q in operator configmap as a boolean: %v", deployCSIKey, err)
		}
	} else {
		// CSI installation is not specified explicitly in the configmap and
		// behaviour is different in case we recognize the StorageCluster API on the cluster.
		storageClusterCRD := &metav1.PartialObjectMetadata{}
		storageClusterCRD.SetGroupVersionKind(
			extv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"),
		)
		storageClusterCRD.Name = "storageclusters.ocs.openshift.io"
		if err = c.get(storageClusterCRD); err != nil {
			if !kerrors.IsNotFound(err) {
				return false, fmt.Errorf("failed to verify existence of storagecluster crd: %v", err)
			}
			// storagecluster CRD doesn't exist
			deployCSI = true
		} else {
			// storagecluster CRD exists and don't deploy CSI until explicitly mentioned in the configmap
			deployCSI = false
		}
	}

	return deployCSI, nil
}

func (c *OperatorConfigMapReconciler) getNoobaaSubManagementConfig() bool {
	valAsString, ok := c.operatorConfigMap.Data[manageNoobaaSubKey]
	if !ok {
		//while feature in dev preview returning false to disable feature by default.
		return false
	}
	val, err := strconv.ParseBool(valAsString)
	if err != nil {
		c.log.Error(
			err,
			"Unsupported value under manageNoobaaSubscription key",
		)
		//while feature in dev preview returning false to disable feature by default.
		return false
	}
	return val
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
	noobaaSubscription, err := c.getSubscriptionByPackageName("noobaa-operator")
	if err != nil {
		return err
	}
	desiredSubChannel := utils.GetNoobaaOperatorChannel()
	if desiredSubChannel != noobaaSubscription.Spec.Channel {
		noobaaSubscription.Spec.Channel = desiredSubChannel
		if err := c.update(noobaaSubscription); err != nil {
			return fmt.Errorf("failed to update subscription channel of 'noobaa-operator' to %v: %v", c.subscriptionChannel, err)
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
			return fmt.Errorf("failed to update subscription channel of 'csi-addons' to %v: %v", c.subscriptionChannel, err)
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

func (c *OperatorConfigMapReconciler) getDesiredSubscriptionChannel() (string, error) {

	storageClients := &v1alpha1.StorageClientList{}
	if err := c.list(storageClients); err != nil {
		return "", fmt.Errorf("failed to list storageclients: %v", err)
	}

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

func (c *OperatorConfigMapReconciler) deleteCSI() error {
	if err := csi.DeleteCSIDriver(c.ctx, c.Client, csi.GetCephFSDriverName()); err != nil && !kerrors.IsNotFound(err) {
		c.log.Error(err, "unable to delete cephfs CSIDriver")
		return err
	}
	if err := csi.DeleteCSIDriver(c.ctx, c.Client, csi.GetRBDDriverName()); err != nil && !kerrors.IsNotFound(err) {
		c.log.Error(err, "unable to delete rbd CSIDriver")
		return err
	}

	c.scc = &secv1.SecurityContextConstraints{}
	c.scc.Name = csi.SCCName
	if err := c.delete(c.scc); err != nil {
		c.log.Error(err, "unable to delete SCC")
		return err
	}
	return nil
}
