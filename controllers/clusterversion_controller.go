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

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// ClusterVersionReconciler reconciles a ClusterVersion object
type ClusterVersionReconciler struct {
	client.Client
	Log    klog.Logger
	Scheme *runtime.Scheme
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterVersionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := mgr.Add(manager.RunnableFunc(func(context.Context) error {
		clusterVersion, err := DetermineOpenShiftVersion(r.Client)
		if err != nil {
			return err
		}

		return r.ensureCsiDrivers(clusterVersion)
	}))
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&configv1.ClusterVersion{}).
		Complete(r)
}

// DetermineOpenShiftVersion returns the desired version of OCP we're running on
func DetermineOpenShiftVersion(client client.Client) (string, error) {
	// Determine ocp version
	clusterVersionList := configv1.ClusterVersionList{}
	if err := client.List(context.TODO(), &clusterVersionList); err != nil {
		return "", err
	}
	clusterVersion := ""
	for _, version := range clusterVersionList.Items {
		clusterVersion = version.Status.Desired.Version
	}
	return clusterVersion, nil
}

//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=deployments/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=daemonsets/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

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
	return nil
}
