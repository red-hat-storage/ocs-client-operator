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
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	// The embed package is required for the prometheus rule files
	_ "embed"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/internal/controller/alert"
	"github.com/red-hat-storage/ocs-client-operator/pkg/console"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	csiopv1 "github.com/ceph/ceph-csi-operator/api/v1"
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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/version"
	k8sYAML "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	//go:embed pvc-rules.yaml
	pvcPrometheusRules string
	//go:embed client-alert-rules.yaml
	clientAlertPrometheusRules  string
	subPackageIndexerRegistered bool
)

const (
	operatorConfigMapName = "ocs-client-operator-config"
	// ClusterVersionName is the name of the ClusterVersion object in the
	// openshift cluster.
	clusterVersionName                = "version"
	manageNoobaaSubKey                = "manageNoobaaSubscription"
	disableVersionChecksKey           = "disableVersionChecks"
	disableInstallPlanAutoApprovalKey = "disableInstallPlanAutoApproval"
	subscriptionLabelKey              = "managed-by"
	subscriptionLabelValue            = "webhook.subscription.ocs.openshift.io"
	generateRbdOMapInfoKey            = "generateRbdOMapInfo"
	enableRbdDriverKey                = "enableRbdDriver"
	enableCephFsDriverKey             = "enableCephFsDriver"
	enableNfsDriverKey                = "enableNfsDriver"

	// AlertPollIntervalKey is the ConfigMap key for the client alert polling interval.
	AlertPollIntervalKey = "alertPollInterval"

	operatorConfigMapFinalizer = "ocs-client-operator.ocs.openshift.io/storageused"
	subPackageIndexName        = "index:subscriptionPackage"
	csiImagesConfigMapLabel    = "ocs.openshift.io/csi-images-version"
	cniNetworksAnnotationKey   = "k8s.v1.cni.cncf.io/networks"
	noobaaCrdName              = "noobaas.noobaa.io"
	noobaaCrName               = "noobaa-remote"

	// disableS3EndpointProxyKey, if true, disables deploying the s3 endpoint reverse proxy for the local/internal client.
	disableS3EndpointProxyKey    = "disableS3EndpointProxy"
	s3EndpointsConfigMapLabelKey = "ocs.openshift.io/hub-s3-endpoints"

	// nginx proxy config key pattern per client: proxy-<clientuid>.conf (all locations for this client in one key).
	nginxProxyConfigKeyFmt = "proxy-%s.conf"

	// default system CA bundle used by nginx for TLS verification.
	defaultCertsPath = "/etc/pki/tls/certs/ca-bundle.crt"
	// optional Secret for custom CA certs; keys are "<clientuid>-<exposeas>.crt".
	s3EndpointCASecretName  = "ocs-client-operator-console-s3-endpoint-ca-certs"
	s3EndpointCertKeySuffix = ".crt"
	// mount path for custom CA secret in the console pod.
	s3EndpointCertsMountPath = "/etc/ssl/certs/s3-endpoint-ca-certs"
)

// ConfigMapData value from the provider that contains the s3 endpoint info (key is the unique identifier, using which the endpoint is exposed).
type s3EndpointConfig struct {
	EndpointURL string `json:"endpointUrl"`
}

// OperatorConfigMapReconciler reconciles a ClusterVersion object
type OperatorConfigMapReconciler struct {
	client.Client
	OperatorNamespace        string
	ConsolePort              int32
	Scheme                   *runtime.Scheme
	AvailableCrds            map[string]bool
	UpdateAlertPollInterval  func(time.Duration)

	log                 logr.Logger
	ctx                 context.Context
	operatorConfigMap   *corev1.ConfigMap
	consoleDeployment   *appsv1.Deployment
	subscriptionChannel string
}

// SetupWithManager sets up the controller with the Manager.
func (c *OperatorConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	if err := addSubscriptionPackageIndexer(ctx, mgr); err != nil {
		return err
	}

	clusterVersionPredicates := builder.WithPredicates(
		predicate.GenerationChangedPredicate{},
	)

	configMapPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(obj client.Object) bool {
				if obj.GetNamespace() != c.OperatorNamespace {
					return false
				}

				if obj.GetName() == operatorConfigMapName {
					return true
				}

				labels := obj.GetLabels()
				return labels != nil && labels[s3EndpointsConfigMapLabelKey] == strconv.FormatBool(true)
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

	s3EndpointCASecretPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(obj client.Object) bool {
				return obj.GetNamespace() == c.OperatorNamespace && obj.GetName() == s3EndpointCASecretName
			},
		),
	)

	storageClientStatusPredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			oldObj := e.ObjectOld.(*v1alpha1.StorageClient)
			newObj := e.ObjectNew.(*v1alpha1.StorageClient)
			return !reflect.DeepEqual(oldObj.Status, newObj.Status)
		},
	}

	generationChangePredicate := predicate.GenerationChangedPredicate{}

	enqueueOwnerConfigMapRequest := handler.EnqueueRequestForOwner(
		mgr.GetScheme(),
		mgr.GetRESTMapper(),
		&corev1.ConfigMap{},
		handler.OnlyControllerOwner(),
	)

	bldr := ctrl.NewControllerManagedBy(mgr).
		Named("OperatorConfigMapReconciler").
		Watches(
			&corev1.ConfigMap{},
			enqueueConfigMapRequest,
			configMapPredicates,
		).
		Watches(
			&corev1.Service{},
			enqueueOwnerConfigMapRequest,
			servicePredicate,
		).
		Watches(
			&csiopv1.OperatorConfig{},
			enqueueOwnerConfigMapRequest,
			builder.WithPredicates(generationChangePredicate),
		).
		Watches(
			&csiopv1.Driver{},
			enqueueOwnerConfigMapRequest,
			builder.WithPredicates(generationChangePredicate),
		).
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
		Watches(
			&v1alpha1.StorageClient{},
			enqueueConfigMapRequest,
			builder.WithPredicates(
				predicate.Or(
					predicate.AnnotationChangedPredicate{},
					storageClientStatusPredicate,
				),
			),
		).
		Watches(
			&corev1.Secret{},
			enqueueConfigMapRequest,
			s3EndpointCASecretPredicates,
		)

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
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=configmaps/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=list;watch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
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
//+kubebuilder:rbac:groups=config.openshift.io,resources=infrastructures,verbs=get;list;watch

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (c *OperatorConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c.ctx = ctx
	c.log = log.FromContext(ctx, "OperatorConfigMap", req)
	c.log.Info("Reconciling OperatorConfigMap")

	crd := &metav1.PartialObjectMetadata{}
	crd.SetGroupVersionKind(extv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	crd.Name = MaintenanceModeCRDName
	if err := c.Get(ctx, client.ObjectKeyFromObject(crd), crd); client.IgnoreNotFound(err) != nil {
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

	alertPollInterval := alert.DefaultPollInterval
	if val := c.operatorConfigMap.Data[AlertPollIntervalKey]; val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			alertPollInterval = d
		} else {
			c.log.Error(err, "failed to parse alertPollInterval, using default", "value", alertPollInterval)
		}
	}
	c.UpdateAlertPollInterval(alertPollInterval)

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
			if err := c.Update(c.ctx, c.operatorConfigMap); err != nil {
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

		if err := c.reconcileODFSnapshotterSubscription(); err != nil {
			c.log.Error(err, "unable to reconcile ODF External Snapshotter Operator subscription")
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

		clientAlertRule := &monitoringv1.PrometheusRule{}
		if err := k8sYAML.NewYAMLOrJSONDecoder(bytes.NewBufferString(string(clientAlertPrometheusRules)), 1000).Decode(clientAlertRule); err != nil {
			c.log.Error(err, "Unable to retrieve client alert prometheus rules.", "prometheusRule", klog.KRef(clientAlertRule.Namespace, clientAlertRule.Name))
			return ctrl.Result{}, err
		}

		clientAlertRule.SetNamespace(c.OperatorNamespace)

		err = c.createOrUpdate(clientAlertRule, func() error {
			applyLabels(c.operatorConfigMap.Data["OCS_METRICS_LABELS"], &clientAlertRule.ObjectMeta)
			return c.own(clientAlertRule)
		})
		if err != nil {
			c.log.Error(err, "failed to create/update client alert prometheus rules")
			return ctrl.Result{}, err
		}

		c.log.Info("client alert prometheus rules deployed", "prometheusRule", klog.KRef(clientAlertRule.Namespace, clientAlertRule.Name))

	} else {
		// deletion phase
		if err := c.deletionPhase(); err != nil {
			return ctrl.Result{}, err
		}

		//remove finalizer
		if controllerutil.RemoveFinalizer(c.operatorConfigMap, operatorConfigMapFinalizer) {
			if err := c.Update(c.ctx, c.operatorConfigMap); err != nil {
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

func (c *OperatorConfigMapReconciler) shouldEnableDriver(driverKey string) bool {
	enableDriverVal, exists := c.operatorConfigMap.Data[driverKey]
	if !exists {
		return false
	}
	enableDriver, err := strconv.ParseBool(enableDriverVal)
	if err != nil {
		c.log.Error(err, "failed to parse configmap key data", "key", driverKey)
		return false
	}
	return enableDriver
}

// getTopologyLabels returns a map of topology labels from the storage clients status and if the
// storage clients status does not have any topology labels, it uses the configmap default values.
func (c *OperatorConfigMapReconciler) getTopologyLabels(storageClients *v1alpha1.StorageClientList) map[string]struct{} {
	topologyDomainLablesSet := map[string]struct{}{}

	// First, merge topology keys from StorageClient Status
	// Multiple storage clients hubs can have different topology keys
	for i := range storageClients.Items {
		storageClient := &storageClients.Items[i]
		if storageClient.Status.RbdDriverRequirements != nil {
			// if the topology domain labels value is not empty, it should be a list of labels
			// e.g. "region,zone"
			for _, label := range storageClient.Status.RbdDriverRequirements.TopologyDomainLabels {
				topologyDomainLablesSet[label] = struct{}{}
			}
		}
	}

	// If no topology labels from StorageClient status, use ConfigMap defaults
	if len(topologyDomainLablesSet) == 0 {
		if configMapTopologyLabels := c.operatorConfigMap.Data[utils.TopologyFailureDomainLabelsKey]; configMapTopologyLabels != "" {
			for label := range strings.SplitSeq(configMapTopologyLabels, ",") {
				label = strings.TrimSpace(label)
				if label != "" {
					topologyDomainLablesSet[label] = struct{}{}
				}
			}
		}
	}

	return topologyDomainLablesSet
}

func (c *OperatorConfigMapReconciler) reconcileDelegatedCSI(storageClients *v1alpha1.StorageClientList) error {
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

	isTnfCluster, err := c.checkIfTNFCluster()
	if err != nil {
		return err
	}

	cniNetworkAnnotationValue := ""
	topologyDomainLablesSet := c.getTopologyLabels(storageClients)

	for i := range storageClients.Items {
		storageClient := &storageClients.Items[i]
		annotations := storageClient.GetAnnotations()
		if annotationValue := annotations[cniNetworksAnnotationKey]; annotationValue != "" {
			if cniNetworkAnnotationValue != "" {
				return fmt.Errorf("only one client with CNI network annotation value is supported")
			}
			cniNetworkAnnotationValue = annotationValue
		}
	}

	// csi operator config
	cmName, err := c.getImageSetConfigMapName(historyRecord.Version)
	if err != nil {
		return fmt.Errorf("failed to get desired imageset configmap name: %v", err)
	}
	csiOperatorConfig := &csiopv1.OperatorConfig{}
	csiOperatorConfig.Name = templates.CSIOperatorConfigName
	csiOperatorConfig.Namespace = c.OperatorNamespace
	if err := c.createOrUpdate(csiOperatorConfig, func() error {
		if err := c.own(csiOperatorConfig); err != nil {
			return fmt.Errorf("failed to own csi operator config: %v", err)
		}
		templates.CSIOperatorConfigSpec.DeepCopyInto(&csiOperatorConfig.Spec)
		driverSpecDefaults := csiOperatorConfig.Spec.DriverSpecDefaults
		if isTnfCluster {
			templates.CSIOperatorTNFControllerPluginResourceSpec.DeepCopyInto(&driverSpecDefaults.ControllerPlugin.Resources)
			templates.CSIOperatorTNFNodePluginResourceSpec.DeepCopyInto(&driverSpecDefaults.NodePlugin.Resources)
		}
		driverSpecDefaults.ImageSet = &corev1.LocalObjectReference{Name: cmName}
		driverSpecDefaults.ClusterName = ptr.To(string(clusterVersion.Spec.ClusterID))
		if c.AvailableCrds[VolumeGroupSnapshotClassCrdName] {
			driverSpecDefaults.SnapshotPolicy = csiopv1.VolumeGroupSnapshotPolicy
		}
		if cniNetworkAnnotationValue != "" {
			if driverSpecDefaults.ControllerPlugin.Annotations == nil {
				driverSpecDefaults.ControllerPlugin.Annotations = map[string]string{}
			}
			driverSpecDefaults.ControllerPlugin.Annotations[cniNetworksAnnotationKey] = cniNetworkAnnotationValue
		}
		if len(topologyDomainLablesSet) > 0 {
			driverSpecDefaults.NodePlugin.Topology = &csiopv1.TopologySpec{
				DomainLabels: slices.Collect(maps.Keys(topologyDomainLablesSet)),
			}
		}
		driverSpecDefaults.GenerateOMapInfo = ptr.To(c.shouldGenerateRBDOmapInfo())
		return nil
	}); err != nil {
		return fmt.Errorf("failed to reconcile csi operator config: %v", err)
	}

	enableRbdDriver := c.shouldEnableDriver(enableRbdDriverKey)
	enableCephFsDriver := c.shouldEnableDriver(enableCephFsDriverKey)
	enableNfsDriver := c.shouldEnableDriver(enableNfsDriverKey)

	var useHostNetForRbdCtrlPlugin, useHostNetForCephFsCtrlPlugin, useHostNetForNfsCtrlPlugin bool

	// if the storage client status has the driver requirements info, then it has higher precedence than the configmap.
	for i := range storageClients.Items {
		if storageClients.Items[i].Status.RbdDriverRequirements != nil {
			enableRbdDriver = true
			if useHostNetwork := storageClients.Items[i].Status.RbdDriverRequirements.CtrlPluginHostNetwork; useHostNetwork != nil {
				useHostNetForRbdCtrlPlugin = useHostNetForRbdCtrlPlugin || ptr.Deref(useHostNetwork, false)
			}
		}
		if storageClients.Items[i].Status.CephFsDriverRequirements != nil {
			enableCephFsDriver = true
			if useHostNetwork := storageClients.Items[i].Status.CephFsDriverRequirements.CtrlPluginHostNetwork; useHostNetwork != nil {
				useHostNetForCephFsCtrlPlugin = useHostNetForCephFsCtrlPlugin || ptr.Deref(useHostNetwork, false)
			}
		}
		if storageClients.Items[i].Status.NfsDriverRequirements != nil {
			enableNfsDriver = true
			if useHostNetwork := storageClients.Items[i].Status.NfsDriverRequirements.CtrlPluginHostNetwork; useHostNetwork != nil {
				useHostNetForNfsCtrlPlugin = useHostNetForNfsCtrlPlugin || ptr.Deref(useHostNetwork, false)
			}
		}
	}

	// ceph rbd driver config
	if enableRbdDriver {
		rbdDriver := &csiopv1.Driver{}
		rbdDriver.Name = templates.RBDDriverName
		rbdDriver.Namespace = c.OperatorNamespace
		if err := c.createOrUpdate(rbdDriver, func() error {
			if err := c.own(rbdDriver); err != nil {
				return fmt.Errorf("failed to own csi rbd driver: %v", err)
			}
			if rbdDriver.Spec.ControllerPlugin == nil {
				rbdDriver.Spec.ControllerPlugin = &csiopv1.ControllerPluginSpec{}
			}
			rbdDriver.Spec.ControllerPlugin.HostNetwork = ptr.To(useHostNetForRbdCtrlPlugin)
			return nil
		}); err != nil {
			return fmt.Errorf("failed to reconcile rbd driver: %v", err)
		}
	}

	// ceph fs driver config
	if enableCephFsDriver {
		cephFsDriver := &csiopv1.Driver{}
		cephFsDriver.Name = templates.CephFsDriverName
		cephFsDriver.Namespace = c.OperatorNamespace
		if err := c.createOrUpdate(cephFsDriver, func() error {
			if err := c.own(cephFsDriver); err != nil {
				return fmt.Errorf("failed to own csi cephfs driver: %v", err)
			}
			if cephFsDriver.Spec.ControllerPlugin == nil {
				cephFsDriver.Spec.ControllerPlugin = &csiopv1.ControllerPluginSpec{}
			}
			cephFsDriver.Spec.ControllerPlugin.HostNetwork = ptr.To(useHostNetForCephFsCtrlPlugin)
			return nil
		}); err != nil {
			return fmt.Errorf("failed to reconcile cephfs driver: %v", err)
		}
	}

	// nfs driver config
	if enableNfsDriver {
		nfsDriver := &csiopv1.Driver{}
		nfsDriver.Name = templates.NfsDriverName
		nfsDriver.Namespace = c.OperatorNamespace
		if err := c.createOrUpdate(nfsDriver, func() error {
			if err := c.own(nfsDriver); err != nil {
				return fmt.Errorf("failed to own csi nfs driver: %v", err)
			}
			if nfsDriver.Spec.ControllerPlugin == nil {
				nfsDriver.Spec.ControllerPlugin = &csiopv1.ControllerPluginSpec{}
			}
			nfsDriver.Spec.ControllerPlugin.HostNetwork = ptr.To(useHostNetForNfsCtrlPlugin)
			return nil
		}); err != nil {
			return fmt.Errorf("failed to reconcile nfs driver: %v", err)
		}
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
	_, err := c.createOrUpdateWithResult(obj, f)
	return err
}

func (c *OperatorConfigMapReconciler) createOrUpdateWithResult(obj client.Object, f controllerutil.MutateFn) (controllerutil.OperationResult, error) {
	result, err := controllerutil.CreateOrUpdate(c.ctx, c.Client, obj, f)
	if err != nil {
		return result, err
	}
	c.log.Info("successfully created or updated", "operation", result, "name", obj.GetName())
	return result, nil
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

	nginxConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      console.NginxConfigMapName,
			Namespace: c.OperatorNamespace,
		},
	}
	nginxConfigMapResult, err := c.createOrUpdateWithResult(nginxConfigMap, func() error {
		desiredData, buildErr := c.buildDesiredNginxDataWithProxies()
		if buildErr != nil {
			return buildErr
		}
		nginxConfigMap.Data = desiredData
		return controllerutil.SetControllerReference(c.consoleDeployment, nginxConfigMap, c.Scheme)
	})
	if err != nil {
		c.log.Error(err, "failed to create nginx config map")
		return err
	}

	if nginxConfigMapResult == controllerutil.OperationResultCreated || nginxConfigMapResult == controllerutil.OperationResultUpdated {
		c.log.Info("nginx ConfigMap data changed, restarting console pod to pick up new config")
		// Console pod must restart (or nginx must reload) to pick up updated mounted config files.
		// Restart failures are currently logged only. If restart fails once and config does not change again,
		// subsequent reconciles will not retry because this block is gated by data-diff check.
		// ToDo: If issue is prominent, explore reliable retry semantics, like restart based on marker/hash or graceful nginx reload.
		// Note: checksum rollout via Deployment pod-template annotation is not reliable as Deployment is currently OLM/CSV-owned.
		c.restartConsolePodsByLabel()
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

func (c *OperatorConfigMapReconciler) restartConsolePodsByLabel() {
	podList := &corev1.PodList{}
	if err := c.list(
		podList,
		client.InNamespace(c.OperatorNamespace),
		client.MatchingLabels{console.AppNameLabelKey: console.DeploymentName},
	); err != nil {
		c.log.Error(err, "failed to list console pods by label selector", "namespace", c.OperatorNamespace)
		return
	}
	if len(podList.Items) == 0 {
		c.log.Info("no console pods found for restart", "namespace", c.OperatorNamespace)
		return
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if err := c.delete(pod); err != nil {
			c.log.Error(err, "failed to delete console pod", "pod", pod.Name, "namespace", c.OperatorNamespace)
		}
	}
}

func (c *OperatorConfigMapReconciler) buildDesiredNginxDataWithProxies() (map[string]string, error) {
	out := map[string]string{
		// Root config is mandatory for nginx to start. Proxy configs (per client) are optional.
		"nginx.conf": console.GetNginxRootConf(),
	}

	if c.operatorConfigMap.Data != nil {
		if disableS3EndpointProxyValue, ok := c.operatorConfigMap.Data[disableS3EndpointProxyKey]; ok {
			disableS3EndpointProxy, err := strconv.ParseBool(disableS3EndpointProxyValue)
			if err != nil {
				return nil, fmt.Errorf("unsupported value under disableS3EndpointProxy key: %w", err)
			}
			if disableS3EndpointProxy {
				c.log.Info("s3 endpoint proxy disabled via operator ConfigMap", "key", disableS3EndpointProxyKey)
				return out, nil
			}
		}
	}

	err := c.computeDesiredProxyConfigByKey(out)
	return out, err
}

func (c *OperatorConfigMapReconciler) computeDesiredProxyConfigByKey(out map[string]string) error {
	extList := &corev1.ConfigMapList{}
	if err := c.list(extList,
		client.InNamespace(c.OperatorNamespace),
		client.MatchingLabels{s3EndpointsConfigMapLabelKey: strconv.FormatBool(true)},
	); err != nil {
		return fmt.Errorf("list s3 endpoints ConfigMaps: %w", err)
	}

	for i := range extList.Items {
		cm := &extList.Items[i]
		uniqueIdentifier := cm.Name
		configKey := fmt.Sprintf(nginxProxyConfigKeyFmt, uniqueIdentifier)
		endpoints, err := parseEndpointConfigs(cm.Data)
		if err != nil {
			return fmt.Errorf("parse endpoints ConfigMap %s: %w", client.ObjectKeyFromObject(cm), err)
		}
		content, err := c.buildS3EndpointProxyConfigForClient(uniqueIdentifier, endpoints)
		if err != nil {
			return err
		}
		if content != "" {
			out[configKey] = content
		}
	}
	return nil
}

func parseEndpointConfigs(data map[string]string) (map[string]s3EndpointConfig, error) {
	out := make(map[string]s3EndpointConfig, len(data))
	for exposeAs, rawCfg := range data {
		trimmedExposeAs := strings.TrimSpace(exposeAs)
		if trimmedExposeAs == "" || strings.TrimSpace(rawCfg) == "" {
			continue
		}

		cfg := s3EndpointConfig{}
		if err := json.Unmarshal([]byte(rawCfg), &cfg); err != nil {
			return nil, fmt.Errorf("decode endpoint config for key %q: %w", trimmedExposeAs, err)
		}
		out[trimmedExposeAs] = cfg
	}
	return out, nil
}

func (c *OperatorConfigMapReconciler) buildS3EndpointProxyConfigForClient(uniqueIdentifier string, endpoints map[string]s3EndpointConfig) (string, error) {
	if len(endpoints) == 0 {
		return "", nil
	}

	secret := &corev1.Secret{}
	secret.Namespace = c.OperatorNamespace
	secret.Name = s3EndpointCASecretName
	if err := c.get(secret); client.IgnoreNotFound(err) != nil {
		return "", fmt.Errorf("failed to get s3 endpoint CA secret %q: %w", s3EndpointCASecretName, err)
	}
	hasCustomCA := secret.UID != ""

	var sb strings.Builder
	exposeAsKeys := make([]string, 0, len(endpoints))
	for exposeAs := range endpoints {
		exposeAsKeys = append(exposeAsKeys, exposeAs)
	}
	// Processing endpoints in a fixed order so the generated nginx config "string" wouldn't change unless the actual endpoint data changed.
	// This is to avoid unnecessary pod restarts, since restarts are triggered when changes are detected.
	sort.Strings(exposeAsKeys)
	for _, exposeAs := range exposeAsKeys {
		cfg := endpoints[exposeAs]
		endpointURL := strings.TrimSpace(cfg.EndpointURL)
		if endpointURL == "" {
			c.log.Info("skipping empty endpoint URL", "exposeAs", exposeAs)
			continue
		}

		parsed, err := url.Parse(endpointURL)
		if err != nil {
			c.log.Error(err, "skipping invalid endpoint URL", "url", endpointURL, "exposeAs", exposeAs)
			continue
		}

		if parsed.Scheme != "https" {
			c.log.Info("skipping non-HTTPS endpoint", "url", endpointURL, "exposeAs", exposeAs)
			continue
		}

		endpointHost := parsed.Host
		certsPath := defaultCertsPath
		certKey := uniqueIdentifier + "-" + exposeAs + s3EndpointCertKeySuffix
		if hasCustomCA {
			if _, ok := secret.Data[certKey]; ok {
				certsPath = s3EndpointCertsMountPath + "/" + certKey
			}
		}

		snippet, err := console.GetNginxProxyConf(
			uniqueIdentifier,
			exposeAs,
			endpointURL,
			endpointHost,
			certsPath,
		)
		if err != nil {
			return "", fmt.Errorf("failed to build proxy config for %q: %w", exposeAs, err)
		}
		sb.WriteString(snippet)
	}
	return sb.String(), nil
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
		wh := &whConfig.Webhooks[0]
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

	clientSubscription, err := getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "ocs-client-operator")
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
	noobaaSubscription, err := getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "mcg-operator")
	if kerrors.IsNotFound(err) {
		noobaaSubscription, err = getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "noobaa-operator")
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
	addonsSubscription, err := getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "odf-csi-addons-operator")
	if kerrors.IsNotFound(err) {
		addonsSubscription, err = getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "csi-addons")
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
	cephCsiOperatorSubscription, err := getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "cephcsi-operator")
	if kerrors.IsNotFound(err) {
		cephCsiOperatorSubscription, err = getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "ceph-csi-operator")
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
	recipeOperatorSubscription, err := getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "recipe")
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

func (c *OperatorConfigMapReconciler) reconcileODFSnapshotterSubscription() error {
	odfSnapshotterSubscription, err := getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "odf-external-snapshotter-operator")
	if err != nil {
		return err
	}
	if c.subscriptionChannel != "" && c.subscriptionChannel != odfSnapshotterSubscription.Spec.Channel {
		odfSnapshotterSubscription.Spec.Channel = c.subscriptionChannel
		if err := c.update(odfSnapshotterSubscription); err != nil {
			return fmt.Errorf("failed to update subscription channel of 'odf-external-snapshotter-operator' to %v: %v", c.subscriptionChannel, err)
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

func getSubscriptionByPackageName(
	ctx context.Context,
	kubeClient client.Client,
	namespace string,
	pkgName string,
) (*opv1a1.Subscription, error) {
	subList := &opv1a1.SubscriptionList{}
	if err := kubeClient.List(
		ctx,
		subList,
		client.MatchingFields{subPackageIndexName: pkgName},
		client.InNamespace(namespace),
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

	csvList := &metav1.PartialObjectMetadataList{}
	csvList.SetGroupVersionKind(opv1a1.SchemeGroupVersion.WithKind("ClusterServiceVersion"))
	if err := c.list(csvList, client.InNamespace(c.OperatorNamespace)); err != nil {
		return fmt.Errorf("failed to list csv: %v", err)
	}

	// If client is installed alongside the odf-op and we don't need to remove noobaa csv and subs
	if slices.ContainsFunc(csvList.Items, func(csv metav1.PartialObjectMetadata) bool {
		return strings.HasPrefix(csv.Name, "odf-operator")
	}) {
		return nil
	}

	mcgCsvList := utils.Filter(csvList.Items, func(csv *metav1.PartialObjectMetadata) bool {
		return strings.HasPrefix(csv.Name, "mcg-operator")
	})
	for i := range mcgCsvList {
		csv := &mcgCsvList[i]
		if csv.GetDeletionTimestamp().IsZero() {
			if err := c.delete(csv); err != nil {
				c.log.Error(err, "failed to delete noobaa operator csv")
				return err
			}
		}
	}

	noobaaSubscription, err := getSubscriptionByPackageName(c.ctx, c.Client, c.OperatorNamespace, "mcg-operator")
	if client.IgnoreNotFound(err) != nil {
		return err
	} else if noobaaSubscription == nil {
		return nil
	}
	if noobaaSubscription.GetDeletionTimestamp().IsZero() {
		if err = c.delete(noobaaSubscription); err != nil {
			return err
		}
	}

	return nil
}

func addSubscriptionPackageIndexer(ctx context.Context, mgr ctrl.Manager) error {
	if subPackageIndexerRegistered {
		return nil
	}

	if err := mgr.GetCache().IndexField(ctx, &opv1a1.Subscription{}, subPackageIndexName, func(o client.Object) []string {
		if sub := o.(*opv1a1.Subscription); sub != nil {
			return []string{sub.Spec.Package}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("unable to set up FieldIndexer for subscription package name: %v", err)
	}

	subPackageIndexerRegistered = true
	return nil
}

func (c *OperatorConfigMapReconciler) checkIfTNFCluster() (bool, error) {
	infra := &configv1.Infrastructure{}
	infra.Name = "cluster"
	err := c.Get(c.ctx, client.ObjectKeyFromObject(infra), infra)
	if err != nil {
		return false, err
	}

	if infra.Status.ControlPlaneTopology == "" {
		return false, fmt.Errorf("controlPlaneTopology is not set in infrastructure resource")
	}

	isTnfCluster := infra.Status.ControlPlaneTopology == "DualReplica"
	c.log.Info("Cluster is running in DualReplica topology (TwoNodeFenced)", "DualReplica", isTnfCluster)

	return isTnfCluster, nil
}
