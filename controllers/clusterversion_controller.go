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
	"strings"

	// The embed package is required for the prometheus rule files
	_ "embed"

	"github.com/red-hat-storage/ocs-client-operator/pkg/console"
	"github.com/red-hat-storage/ocs-client-operator/pkg/csi"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"

	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	consolev1alpha1 "github.com/openshift/api/console/v1alpha1"
	secv1 "github.com/openshift/api/security/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
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
)

//go:embed pvc-rules.yaml
var pvcPrometheusRules string

const (
	operatorConfigMapName = "ocs-client-operator-config"
	// ClusterVersionName is the name of the ClusterVersion object in the
	// openshift cluster.
	clusterVersionName = "version"
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
	clusterVersion    configv1.ClusterVersion
	consoleDeployment appsv1.Deployment
	operatorConfigMap corev1.ConfigMap
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
		func(_ context.Context, _ client.Object) []ctrl.Request {
			return []ctrl.Request{{
				NamespacedName: types.NamespacedName{
					Name: clusterVersionName,
				},
			}}
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&configv1.ClusterVersion{}, clusterVersionPredicates).
		Watches(&corev1.ConfigMap{}, enqueueClusterVersionRequest, configMapPredicates).
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

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (c *ClusterVersionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c.ctx = ctx
	c.log = log.FromContext(ctx, "ClusterVersion", req)

	c.log.Info("Loading input resources")

	// Load the cluster version
	c.clusterVersion.Name = req.Name
	if err := c.get(&c.clusterVersion); err != nil {
		return ctrl.Result{}, err
	}

	// Load the console plugin
	c.consoleDeployment.Name = console.DeploymentName
	c.consoleDeployment.Namespace = c.OperatorNamespace
	if err := c.get(&c.consoleDeployment); err != nil {
		c.log.Error(err, "failed to get the deployment for the console")
		return ctrl.Result{}, err
	}

	// Load the operator configmap
	c.operatorConfigMap.Name = operatorConfigMapName
	c.operatorConfigMap.Namespace = c.OperatorNamespace
	if err := c.get(&c.operatorConfigMap); err != nil && !k8serrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	c.log.Info("Starting reconciliation")
	result, err := c.reconcilPhases()
	if err != nil {
		c.log.Error(err, "An error was encountered during reconciliation")
		return ctrl.Result{}, err
	}

	c.log.Info("Reconciliation completed successfully")
	return result, nil
}

func (c *ClusterVersionReconciler) reconcilPhases() (ctrl.Result, error) {

	// Reconcile console related resources
	if err := c.reconcileNginxConfigMap(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileConsoleService(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileConsolePlugin(); err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile CSI related resources
	if err := csi.InitializeSidecars(c.log, c.clusterVersion.Status.Desired.Version); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileSecurityContextConstraints(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileMonConfigMap(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileEncConfigMap(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileCephFSDeployment(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileCephFSDaemonSet(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileRBDDeployment(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileRBDDaemonSet(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileCephFSCSIDrivers(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcileRBDCSIDriver(); err != nil {
		return ctrl.Result{}, err
	}
	if err := c.reconcilePrometheusRule(); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (c *ClusterVersionReconciler) reconcileNginxConfigMap() error {
	c.log.Info("Reconciling NginxConfigMap")

	nginxConfigMap := corev1.ConfigMap{}
	nginxConfigMap.Name = console.NginxConfigMapName
	nginxConfigMap.Namespace = c.OperatorNamespace

	err := c.createOrUpdate(&nginxConfigMap, func() error {
		if err := c.own(&nginxConfigMap); err != nil {
			return err
		}

		console.SetNgnixConfigMapDesiredState(&nginxConfigMap)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create nginx config map")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileConsoleService() error {
	c.log.Info("Reconciling ConsoleService")

	consoleService := corev1.Service{}
	consoleService.Name = console.DeploymentName
	consoleService.Namespace = c.OperatorNamespace

	err := c.createOrUpdate(&consoleService, func() error {
		if err := c.own(&consoleService); err != nil {
			return err
		}

		console.SetConsoleServiceDesiredState(&consoleService, c.ConsolePort)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update service for console")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileConsolePlugin() error {
	c.log.Info("Reconciling ConsolePlugin")

	consolePlugin := consolev1alpha1.ConsolePlugin{}
	consolePlugin.Name = console.PluginName

	err := c.createOrUpdate(&consolePlugin, func() error {
		console.SetConsolePluginDesiredState(&consolePlugin, c.ConsolePort, c.OperatorNamespace)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update consoleplugin")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileSecurityContextConstraints() error {
	c.log.Info("Reconciling SecurityContextConstraints")

	scc := secv1.SecurityContextConstraints{}
	scc.Name = csi.SCCName

	err := c.createOrUpdate(&scc, func() error {
		csi.SetSecurityContextConstraintsDesiredState(&scc, c.OperatorNamespace)
		return nil
	})
	if err != nil {
		c.log.Error(err, "unable to create/update SCC")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileMonConfigMap() error {
	c.log.Info("Reconciling MonConfigMap")

	// create the monitor configmap for the csi drivers but never updates it.
	// This is because the monitor configurations are added to the configmap
	// when user creates storageclassclaims.
	monConfigMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templates.MonConfigMapName,
			Namespace: c.OperatorNamespace,
		},
		Data: map[string]string{
			"config.json": "[]",
		},
	}
	if err := c.own(&monConfigMap); err != nil {
		return err
	}
	if err := c.create(&monConfigMap); err != nil && !k8serrors.IsAlreadyExists(err) {
		c.log.Error(err, "failed to create monitor configmap", "name", monConfigMap.Name)
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileEncConfigMap() error {
	c.log.Info("Reconciling EncConfigMap")

	// create the encryption configmap for the csi driver but never updates it.
	// This is because the encryption configuration are added to the configmap
	// by the users before they create the encryption storageclassclaims.
	encConfigMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templates.EncryptionConfigMapName,
			Namespace: c.OperatorNamespace,
		},
		Data: map[string]string{
			"config.json": "[]",
		},
	}
	if err := c.own(&encConfigMap); err != nil {
		return err
	}
	if err := c.create(&encConfigMap); err != nil && !k8serrors.IsAlreadyExists(err) {
		c.log.Error(err, "failed to create monitor configmap", "name", encConfigMap.Name)
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileCephFSDeployment() error {
	c.log.Info("Reconciling CephFSDeployment")

	cephFSDeployment := appsv1.Deployment{}
	cephFSDeployment.Name = csi.CephFSDeploymentName
	cephFSDeployment.Namespace = c.OperatorNamespace

	err := c.createOrUpdate(&cephFSDeployment, func() error {
		if err := c.own(&cephFSDeployment); err != nil {
			return err
		}
		csi.SetCephFSDeploymentDesiredState(&cephFSDeployment)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update cephfs deployment")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileCephFSDaemonSet() error {
	c.log.Info("Reconciling CephFSDaemonSet")

	cephFSDaemonSet := appsv1.DaemonSet{}
	cephFSDaemonSet.Name = csi.CephFSDaemonSetName
	cephFSDaemonSet.Namespace = c.OperatorNamespace

	err := c.createOrUpdate(&cephFSDaemonSet, func() error {
		if err := c.own(&cephFSDaemonSet); err != nil {
			return err
		}
		csi.SetCephFSDaemonSetDesiredState(&cephFSDaemonSet)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update cephfs daemonset")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileRBDDeployment() error {
	c.log.Info("Reconciling RBDDeployment")

	rbdDeployment := appsv1.Deployment{}
	rbdDeployment.Name = csi.RBDDeploymentName
	rbdDeployment.Namespace = c.OperatorNamespace

	err := c.createOrUpdate(&rbdDeployment, func() error {
		if err := c.own(&rbdDeployment); err != nil {
			return err
		}
		csi.SetRBDDeploymentDesiredState(&rbdDeployment)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update rbd deployment")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileRBDDaemonSet() error {
	c.log.Info("Reconciling RBDDaemonSet")

	rbdDaemonSet := appsv1.DaemonSet{}
	rbdDaemonSet.Name = csi.RBDDaemonSetName
	rbdDaemonSet.Namespace = c.OperatorNamespace

	err := c.createOrUpdate(&rbdDaemonSet, func() error {
		if err := c.own(&rbdDaemonSet); err != nil {
			return err
		}
		csi.SetRBDDaemonSetDesiredState(&rbdDaemonSet)
		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update rbd daemonset")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileCephFSCSIDrivers() error {
	c.log.Info("Reconciling CephFSCSIDrivers")

	// Need to handle deletion of the csiDriver object, we cannot set
	// ownerReference on it as its cluster scoped resource
	cephfsCSIDriver := templates.CephFSCSIDriver.DeepCopy()
	cephfsCSIDriver.Name = csi.GetCephFSDriverName()
	if err := csi.CreateCSIDriver(c.ctx, c.Client, cephfsCSIDriver); err != nil {
		c.log.Error(err, "unable to create cephfs CSIDriver")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcileRBDCSIDriver() error {
	c.log.Info("Reconciling RBDCSIDriver")

	rbdCSIDriver := templates.RbdCSIDriver.DeepCopy()
	rbdCSIDriver.Name = csi.GetRBDDriverName()
	if err := csi.CreateCSIDriver(c.ctx, c.Client, rbdCSIDriver); err != nil {
		c.log.Error(err, "unable to create rbd CSIDriver")
		return err
	}
	return nil
}

func (c *ClusterVersionReconciler) reconcilePrometheusRule() error {
	c.log.Info("Reconciling PrometheusRule")

	prometheusRule := monitoringv1.PrometheusRule{}
	prometheusRule.Name = "prometheus-pvc-rules"
	prometheusRule.Namespace = c.OperatorNamespace

	err := c.createOrUpdate(&prometheusRule, func() error {
		if err := c.own(&prometheusRule); err != nil {
			return err
		}

		parseAndAddLabels(c.operatorConfigMap.Data["OCS_METRICS_LABELS"], &prometheusRule)

		desiredState := monitoringv1.PrometheusRule{}
		decoder := k8sYAML.NewYAMLOrJSONDecoder(bytes.NewBufferString(string(pvcPrometheusRules)), 1000)
		if err := decoder.Decode(&desiredState); err != nil {
			c.log.Error(err, "Unable to retrieve prometheus rules.", "prometheusRule", klog.KRef(prometheusRule.Namespace, prometheusRule.Name))
			return err
		}

		// Copying spec only to prevant the loss of previous metadata
		desiredState.Spec.DeepCopyInto(&prometheusRule.Spec)

		return nil
	})
	if err != nil {
		c.log.Error(err, "failed to create/update prometheus rules")
		return err
	}

	c.log.Info("prometheus rules deployed", "prometheusRule", klog.KRef(prometheusRule.Namespace, prometheusRule.Name))
	return nil

}

func (c *ClusterVersionReconciler) createOrUpdate(obj client.Object, f controllerutil.MutateFn) error {
	result, err := controllerutil.CreateOrUpdate(c.ctx, c.Client, obj, f)
	if err != nil {
		return err
	}
	c.log.Info("successfully created or updated", "operation", result, "name", obj.GetName())
	return nil
}

func (c *ClusterVersionReconciler) get(obj client.Object) error {
	return c.Client.Get(c.ctx, client.ObjectKeyFromObject(obj), obj)
}

func (c *ClusterVersionReconciler) own(obj client.Object) error {
	return controllerutil.SetControllerReference(c.OperatorDeployment, obj, c.Client.Scheme())
}

func (c *ClusterVersionReconciler) create(obj client.Object) error {
	return c.Client.Create(c.ctx, obj)
}

// applyLabels adds labels to object meta, overwriting keys that are already defined.
func parseAndAddLabels(labelsAsString string, obj metav1.Object) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}

	// Loop through the lines and extract key-value pairs
	lines := strings.Split(labelsAsString, "\n")
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		labels[key] = value
	}
}
