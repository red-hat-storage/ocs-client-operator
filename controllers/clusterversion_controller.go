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

package controllers

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	// The embed package is required for the prometheus rule files
	_ "embed"

	"github.com/red-hat-storage/ocs-client-operator/pkg/console"
	"github.com/red-hat-storage/ocs-client-operator/pkg/csi"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	secv1 "github.com/openshift/api/security/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	admrv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sYAML "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
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
	clusterVersionName = "version"

	subscriptionLabelKey   = "managed-by"
	subscriptionLabelValue = "webhook/subscription.ocs.openshift.io"
)

// ClusterVersionReconciler reconciles a ClusterVersion object
type ClusterVersionReconciler struct {
	client.Client
	OperatorDeployment *appsv1.Deployment
	OperatorNamespace  string
	ConsolePort        int32
	Scheme             *runtime.Scheme

	log               logr.Logger
	ctx               context.Context
	consoleDeployment *appsv1.Deployment
	cephFSDeployment  *appsv1.Deployment
	cephFSDaemonSet   *appsv1.DaemonSet
	rbdDeployment     *appsv1.Deployment
	rbdDaemonSet      *appsv1.DaemonSet
	scc               *secv1.SecurityContextConstraints
}

// SetupWithManager sets up the controller with the Manager.
func (c *ClusterVersionReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
	// Reconcile the ClusterVersion object when the operator config map is updated
	enqueueClusterVersionRequest := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, _ client.Object) []reconcile.Request {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Name: clusterVersionName,
				},
			}}
		},
	)

	subscriptionPredicates := builder.WithPredicates(
		predicate.NewPredicateFuncs(
			func(client client.Object) bool {
				return c.OperatorNamespace == client.GetNamespace()
			},
		),
		predicate.GenerationChangedPredicate{},
	)
	return ctrl.NewControllerManagedBy(mgr).
		For(&configv1.ClusterVersion{}, clusterVersionPredicates).
		Watches(&corev1.ConfigMap{}, enqueueClusterVersionRequest, configMapPredicates).
		Watches(&opv1a1.Subscription{}, enqueueClusterVersionRequest, subscriptionPredicates).
		Complete(c)
}

//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=deployments/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=daemonsets/finalizers,verbs=update
//+kubebuilder:rbac:groups="storage.k8s.io",resources=csidrivers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=get;list;watch;create;patch;update
//+kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list;watch;create;update
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=console.openshift.io,resources=consoleplugins,verbs=*
//+kubebuilder:rbac:groups=operators.coreos.com,resources=subscriptions,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;list;update

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (c *ClusterVersionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error
	c.ctx = ctx
	c.log = log.FromContext(ctx, "ClusterVersion", req)
	c.log.Info("Reconciling ClusterVersion")

	if err := c.ensureConsolePlugin(); err != nil {
		c.log.Error(err, "unable to deploy client console")
		return ctrl.Result{}, err
	}

	if err := c.registerWebhook(); err != nil {
		c.log.Error(err, "unable to register subscription validating webhook")
		return ctrl.Result{}, err
	}

	if err := labelClientOperatorSubscription(c); err != nil {
		c.log.Error(err, "unable to label ocs client operator subscription")
		return ctrl.Result{}, err
	}

	instance := configv1.ClusterVersion{}
	if err = c.Client.Get(context.TODO(), req.NamespacedName, &instance); err != nil {
		return ctrl.Result{}, err
	}

	if err := csi.InitializeSidecars(c.log, instance.Status.Desired.Version); err != nil {
		c.log.Error(err, "unable to initialize sidecars")
		return ctrl.Result{}, err
	}

	c.scc = &secv1.SecurityContextConstraints{
		ObjectMeta: metav1.ObjectMeta{
			Name: csi.SCCName,
		},
	}
	err = c.createOrUpdate(c.scc, func() error {
		// TODO: this is a hack to preserve the resourceVersion of the SCC
		resourceVersion := c.scc.ResourceVersion
		csi.GetSecurityContextConstraints(c.OperatorNamespace).DeepCopyInto(c.scc)
		c.scc.ResourceVersion = resourceVersion
		return nil
	})
	if err != nil {
		c.log.Error(err, "unable to create/update SCC")
		return ctrl.Result{}, err
	}

	// create the monitor configmap for the csi drivers but never updates it.
	// This is because the monitor configurations are added to the configmap
	// when user creates storageclassclaims.
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
		return ctrl.Result{}, err
	}
	err = c.create(monConfigMap)
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		c.log.Error(err, "failed to create monitor configmap", "name", monConfigMap.Name)
		return ctrl.Result{}, err
	}

	// create the encryption configmap for the csi driver but never updates it.
	// This is because the encryption configuration are added to the configmap
	// by the users before they create the encryption storageclassclaims.
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
		return ctrl.Result{}, err
	}
	err = c.create(encConfigMap)
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		c.log.Error(err, "failed to create monitor configmap", "name", encConfigMap.Name)
		return ctrl.Result{}, err
	}

	c.cephFSDeployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csi.CephFSDeploymentName,
			Namespace: c.OperatorNamespace,
		},
	}
	err = c.createOrUpdate(c.cephFSDeployment, func() error {
		csi.GetCephFSDeployment(c.OperatorNamespace).DeepCopyInto(c.cephFSDeployment)
		if err := c.own(c.cephFSDeployment); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update cephfs deployment")
		return ctrl.Result{}, err
	}

	c.cephFSDaemonSet = &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csi.CephFSDamonSetName,
			Namespace: c.OperatorNamespace,
		},
	}
	err = c.createOrUpdate(c.cephFSDaemonSet, func() error {
		csi.GetCephFSDaemonSet(c.OperatorNamespace).DeepCopyInto(c.cephFSDaemonSet)
		if err := c.own(c.cephFSDaemonSet); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update cephfs daemonset")
		return ctrl.Result{}, err
	}

	c.rbdDeployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csi.RBDDeploymentName,
			Namespace: c.OperatorNamespace,
		},
	}
	err = c.createOrUpdate(c.rbdDeployment, func() error {
		csi.GetRBDDeployment(c.OperatorNamespace).DeepCopyInto(c.rbdDeployment)
		if err := c.own(c.rbdDeployment); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update rbd deployment")
		return ctrl.Result{}, err
	}

	c.rbdDaemonSet = &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csi.RBDDaemonSetName,
			Namespace: c.OperatorNamespace,
		},
	}
	err = c.createOrUpdate(c.rbdDaemonSet, func() error {
		csi.GetRBDDaemonSet(c.OperatorNamespace).DeepCopyInto(c.rbdDaemonSet)
		if err := c.own(c.rbdDaemonSet); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update rbd daemonset")
		return ctrl.Result{}, err
	}

	// Need to handle deletion of the csiDriver object, we cannot set
	// ownerReference on it as its cluster scoped resource
	cephfsCSIDriver := templates.CephFSCSIDriver.DeepCopy()
	cephfsCSIDriver.ObjectMeta.Name = csi.GetCephFSDriverName()
	err = csi.CreateCSIDriver(c.ctx, c.Client, cephfsCSIDriver)
	if err != nil {
		c.log.Error(err, "unable to create cephfs CSIDriver")
		return ctrl.Result{}, err
	}

	rbdCSIDriver := templates.RbdCSIDriver.DeepCopy()
	rbdCSIDriver.ObjectMeta.Name = csi.GetRBDDriverName()
	err = csi.CreateCSIDriver(c.ctx, c.Client, rbdCSIDriver)
	if err != nil {
		c.log.Error(err, "unable to create rbd CSIDriver")
		return ctrl.Result{}, err
	}

	prometheusRule := &monitoringv1.PrometheusRule{}
	err = k8sYAML.NewYAMLOrJSONDecoder(bytes.NewBufferString(string(pvcPrometheusRules)), 1000).Decode(prometheusRule)
	if err != nil {
		c.log.Error(err, "Unable to retrieve prometheus rules.", "prometheusRule", klog.KRef(prometheusRule.Namespace, prometheusRule.Name))
		return ctrl.Result{}, err
	}

	operatorConfig, err := c.getOperatorConfig()
	if err != nil {
		return ctrl.Result{}, err
	}
	prometheusRule.SetNamespace(c.OperatorNamespace)

	err = c.createOrUpdate(prometheusRule, func() error {
		applyLabels(operatorConfig.Data["OCS_METRICS_LABELS"], &prometheusRule.ObjectMeta)
		return c.own(prometheusRule)
	})
	if err != nil {
		c.log.Error(err, "failed to create/update prometheus rules")
		return ctrl.Result{}, err
	}

	c.log.Info("prometheus rules deployed", "prometheusRule", klog.KRef(prometheusRule.Namespace, prometheusRule.Name))

	return ctrl.Result{}, nil
}

func (c *ClusterVersionReconciler) createOrUpdate(obj client.Object, f controllerutil.MutateFn) error {
	result, err := controllerutil.CreateOrUpdate(c.ctx, c.Client, obj, f)
	if err != nil {
		return err
	}
	c.log.Info("successfully created or updated", "operation", result, "name", obj.GetName())
	return nil
}

func (c *ClusterVersionReconciler) own(obj client.Object) error {
	return controllerutil.SetControllerReference(c.OperatorDeployment, obj, c.Client.Scheme())
}

func (c *ClusterVersionReconciler) create(obj client.Object) error {
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

func (c *ClusterVersionReconciler) getOperatorConfig() (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := c.Client.Get(c.ctx, types.NamespacedName{Name: operatorConfigMapName, Namespace: c.OperatorNamespace}, cm)
	if err != nil && !k8serrors.IsNotFound(err) {
		return nil, err
	}
	return cm, nil
}

func (c *ClusterVersionReconciler) ensureConsolePlugin() error {
	c.consoleDeployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      console.DeploymentName,
			Namespace: c.OperatorNamespace,
		},
	}

	err := c.Client.Get(c.ctx, types.NamespacedName{
		Name:      console.DeploymentName,
		Namespace: c.OperatorNamespace,
	}, c.consoleDeployment)
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

func (c *ClusterVersionReconciler) registerWebhook() error {
	whConfig := &admrv1.ValidatingWebhookConfiguration{}
	whConfig.Name = "subscription.ocs.openshift.io"

	err := c.createOrUpdate(whConfig, func() error {

		// openshift fills in the ca on finding this annotation
		whConfig.Annotations = map[string]string{
			"service.beta.openshift.io/inject-cabundle": "true",
		}

		// take a copy of the template
		wh := *templates.SubscriptionValidatingWebhook.DeepCopy()
		if len(whConfig.Webhooks) != 0 {
			// do not mutate CA bundle that was injected by openshift
			wh.ClientConfig.CABundle = whConfig.Webhooks[0].ClientConfig.CABundle
		}

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

		// send request to the service running in own namespace
		wh.ClientConfig.Service.Namespace = c.OperatorNamespace

		// set the webhook
		whConfig.Webhooks = []admrv1.ValidatingWebhook{wh}

		return nil
	})

	if err != nil {
		return err
	}

	c.log.Info("successfully registered validating webhook")
	return nil
}

func labelClientOperatorSubscription(c *ClusterVersionReconciler) error {
	subscriptionList := &opv1a1.SubscriptionList{}
	err := c.List(c.ctx, subscriptionList, client.InNamespace(c.OperatorNamespace))
	if err != nil {
		return fmt.Errorf("failed to list subscriptions")
	}

	sub := utils.Find(subscriptionList.Items, func(sub *opv1a1.Subscription) bool {
		return sub.Spec.Package == "ocs-client-operator"
	})

	if sub == nil {
		return fmt.Errorf("failed to find subscription with ocs-client-operator package")
	}

	if sub.GetLabels() == nil {
		sub.Labels = map[string]string{}
	}
	sub.Labels[subscriptionLabelKey] = subscriptionLabelValue
	err = c.Update(c.ctx, sub)
	if err != nil {
		return err
	}

	c.log.Info("successfully labelled ocs-client-operator subscription")
	return nil
}
