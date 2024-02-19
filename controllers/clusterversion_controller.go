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
	"time"

	// The embed package is required for the prometheus rule files
	_ "embed"

	"github.com/red-hat-storage/ocs-client-operator/pkg/console"
	"github.com/red-hat-storage/ocs-client-operator/pkg/csi"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"

	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	secv1 "github.com/openshift/api/security/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
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

	ClusterVersionFinalizer = "ocs-client-operator.ocs.openshift.io/ocspvcused"
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
	var err error
	c.ctx = ctx
	c.log = log.FromContext(ctx, "ClusterVersion", req)
	c.log.Info("Reconciling ClusterVersion")

	if err := c.ensureConsolePlugin(); err != nil {
		c.log.Error(err, "unable to deploy client console")
		return ctrl.Result{}, err
	}

	instance := configv1.ClusterVersion{}
	if err = c.Client.Get(context.TODO(), req.NamespacedName, &instance); err != nil {
		return ctrl.Result{}, err
	}

	if instance.GetDeletionTimestamp().IsZero() {

		//ensure finalizer
		if !controllerutil.ContainsFinalizer(&instance, ClusterVersionFinalizer) {
			c.log.Info("finalizer missing on the ClusterVersion resource, adding...")
			controllerutil.AddFinalizer(&instance, ClusterVersionFinalizer)
			if err := c.Client.Update(c.ctx, &instance); err != nil {
				return ctrl.Result{}, err
			}
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
			csi.SetSecurityContextConstraintsDesiredState(c.scc, c.OperatorNamespace)
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
			if err := c.own(c.cephFSDeployment); err != nil {
				return err
			}
			csi.SetCephFSDeploymentDesiredState(c.cephFSDeployment)
			return nil
		})
		if err != nil {
			c.log.Error(err, "failed to create/update cephfs deployment")
			return ctrl.Result{}, err
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
			return ctrl.Result{}, err
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
			return ctrl.Result{}, err
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
	} else {
		// deletion phase
		result, err := c.deletionPhase()
		if err != nil || !result.Requeue || result.RequeueAfter > 0 {
			return result, err
		}

		//remove finalizer
		controllerutil.RemoveFinalizer(&instance, ClusterVersionFinalizer)
		if err := c.Client.Update(c.ctx, &instance); err != nil {
			return ctrl.Result{}, err
		}
		c.log.Info("finallizer removed successfully")
	}

	return ctrl.Result{}, nil
}

func (c *ClusterVersionReconciler) deletionPhase() (reconcile.Result, error) {
	ocsPvsPresent, err := c.hasOCSVolumes()
	if err != nil {
		c.log.Error(err, "unable to verify PVs presence prior deletion of ceph resources")
		return ctrl.Result{}, err
	}
	if ocsPvsPresent {
		c.log.Info("unable to delete ceph resources, PVs consuming client resources are present")
		return ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, nil
	}
	if err := csi.DeleteCSIDriver(c.ctx, c.Client, csi.GetCephFSDriverName()); err != nil && !k8serrors.IsNotFound(err) {
		c.log.Error(err, "unable to delete cephfs CSIDriver")
		return ctrl.Result{}, err
	}
	if err := csi.DeleteCSIDriver(c.ctx, c.Client, csi.GetRBDDriverName()); err != nil && !k8serrors.IsNotFound(err) {
		c.log.Error(err, "unable to delete rbd CSIDriver")
		return ctrl.Result{}, err
	}
	if err := c.Client.Delete(c.ctx, c.scc); err != nil && !k8serrors.IsNotFound(err) {
		c.log.Error(err, "unable to delete SCC")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (c *ClusterVersionReconciler) hasOCSVolumes() (bool, error) {
	// get all the storage class
	storageClassList := storagev1.StorageClassList{}
	if err := c.Client.List(c.ctx, &storageClassList); err != nil {
		return false, fmt.Errorf("unable to list storage classes: %v", err)
	}

	// create a set of storage class names who are using the client's provisioners
	ocsStorageClass := make(map[string]bool)
	for i := range storageClassList.Items {
		storageClass := &storageClassList.Items[i]
		if storageClassList.Items[i].Provisioner == csi.GetCephFSDriverName() || storageClassList.Items[i].Provisioner == csi.GetRBDDriverName() {
			ocsStorageClass[storageClass.Name] = true
		}
	}

	// get all the PVs
	pvList := &corev1.PersistentVolumeList{}
	if err := c.Client.List(c.ctx, pvList); err != nil {
		return false, fmt.Errorf("unable to list persistent volumes: %v", err)
	}

	// check if there are any PVs using client's storage classes
	for i := range pvList.Items {
		scName := pvList.Items[i].Spec.StorageClassName
		if ocsStorageClass[scName] {
			return true, nil
		}
	}
	return false, nil
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
