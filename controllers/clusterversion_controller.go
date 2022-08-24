/*
Copyright 2021 Red Hat OpenShift Data Foundation.

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
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	k8scsi "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ClusterVersionReconciler reconciles a ClusterVersion object
type ClusterVersionReconciler struct {
	client.Client
	Log       klog.Logger
	Scheme    *runtime.Scheme
	Namespace string
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterVersionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&configv1.ClusterVersion{}).
		Complete(r)
}

//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=deployments/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=daemonsets/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="storage.k8s.io",resources=csidrivers,verbs=get;list;watch;create;update;patch;delete

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *ClusterVersionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error

	r.Log = log.FromContext(ctx, "ClusterVersion", req)
	r.Log.Info("Reconciling ClusterVersion")

	instance := configv1.ClusterVersion{}
	if err = r.Client.Get(context.TODO(), req.NamespacedName, &instance); err != nil {
		return ctrl.Result{}, err
	}

	if err = r.ensureCsiDrivers(instance.Status.Desired.Version); err != nil {
		r.Log.Error(err, "Could not ensure compatibility for Ceph-CSI drivers")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ClusterVersionReconciler) ensureCsiDrivers(clusterVersion string) error {

	// create empty encryption configmap required for csi driver to start
	err := r.ensureEncryptionConfigMap()
	if err != nil {
		return err
	}

	// create empty mon configmap required for csi driver to start
	err = r.ensureMonConfigMap()
	if err != nil {
		return err
	}

	csiSidecars := &sideCarContainer{
		namespace:      r.Namespace,
		clusterVersion: clusterVersion,
	}

	ctx := context.TODO()
	rbdDeployment := getRBDControllerDeployment(csiSidecars)
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, rbdDeployment, func() error {
		// TODO need to set ownerRef?
		return nil
	})
	if err != nil {
		r.Log.Error(err, "csi controller reconcile failure", "name", rbdDeployment.Name)
		return err
	}

	r.Log.Info("csi controller", "operation", result, "name", rbdDeployment.Name)

	cephFSDeployment := getCephFSControllerDeployment(csiSidecars)
	result, err = controllerutil.CreateOrUpdate(ctx, r.Client, cephFSDeployment, func() error {
		// TODO need to set ownerRef?
		return nil
	})
	if err != nil {
		r.Log.Error(err, "csi controller reconcile failure", "name", cephFSDeployment.Name)
		return err
	}

	r.Log.Info("csi controller", "operation", result, "name", cephFSDeployment.Name)

	rbdDaemonSet := getRBDDaemonSet(csiSidecars)
	result, err = controllerutil.CreateOrUpdate(ctx, r.Client, rbdDaemonSet, func() error {
		// TODO need to set ownerRef?
		return nil
	})
	if err != nil {
		r.Log.Error(err, "csi plugin reconcile failure", "name", rbdDaemonSet.Name)
		return err
	}

	r.Log.Info("csi plugin", "operation", result, "name", rbdDaemonSet.Name)

	cephFSDaemonSet := getCephFSDaemonSet(csiSidecars)
	result, err = controllerutil.CreateOrUpdate(ctx, r.Client, cephFSDaemonSet, func() error {
		// TODO need to set ownerRef?
		return nil
	})
	if err != nil {
		r.Log.Error(err, "csi plugin reconcile failure", "name", cephFSDaemonSet.Name)
		return err
	}

	r.Log.Info("csi plugin", "operation", result, "name", cephFSDaemonSet.Name)

	// An error in reconciling one CSIDriver should not block
	// reconciliation of the other. An error from either should still
	// requeue the request.

	// ensure CephFs CSIDriver
	cephFsErr := r.ensureCsiDriverCr(
		ctx,
		getCephFSDriverName(r.Namespace),
		k8scsi.ReadWriteOnceWithFSTypeFSGroupPolicy,
		true,
		false,
	)

	// ensure RBD CSIDriver
	rbdErr := r.ensureCsiDriverCr(
		ctx,
		getRBDDriverName(r.Namespace),
		k8scsi.ReadWriteOnceWithFSTypeFSGroupPolicy,
		true,
		false,
	)

	if cephFsErr != nil || rbdErr != nil {
		return fmt.Errorf("error creating CSIDrivers")
	}

	return nil
}

func (r *ClusterVersionReconciler) ensureEncryptionConfigMap() error {
	// create empty encryption configmap required for csi driver to start
	encryptionConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      encryptionConfigMapName,
			Namespace: r.Namespace,
		},
	}
	err := r.Client.Create(context.TODO(), encryptionConfigMap)
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		r.Log.Error(err, "encryption configmap reconcile failure", "name", encryptionConfigMap.Name)
		return err
	}

	r.Log.Info("successfully created encryption configmap", "name", encryptionConfigMap.Name)
	return nil
}

func (r *ClusterVersionReconciler) ensureMonConfigMap() error {
	// create empty mon configmap required for csi driver to start
	monConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      monConfigMapName,
			Namespace: r.Namespace,
		},
		Data: map[string]string{
			"config.json": "[]",
		},
	}
	err := r.Client.Create(context.TODO(), monConfigMap)
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		r.Log.Error(err, "mon configmap reconcile failure", "name", monConfigMap.Name)
		return err
	}

	r.Log.Info("successfully created mon configmap", "name", monConfigMap.Name)
	return nil
}
