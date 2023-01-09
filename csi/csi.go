/*
Copyright 2022 Red Hat, Inc.

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

package csi

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	"github.com/red-hat-storage/ocs-client-operator/templates"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// +kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;create;update;delete
// +kubebuilder:rbac:groups="apps",resources=deployments/finalizers,verbs=update
// +kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;create;update;delete
// +kubebuilder:rbac:groups="apps",resources=daemonsets/finalizers,verbs=update
// +kubebuilder:rbac:groups="storage.k8s.io",resources=csidrivers,verbs=get;create;update;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;update;delete
// +kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=get;watch;create;patch;update
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;create;update;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts/finalizers,verbs=update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind;escalate;get;create;update;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles/finalizers,verbs=update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=bind;escalate;get;create;update;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=bind;escalate;get;create;update;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles/finalizers,verbs=update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=bind;escalate;get;create;update;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings/finalizers,verbs=update

// Proving escalate permission to operator as its required for creating
// clusterrole and clusterrolebinding. More info:
// https://kubernetes.io/docs/reference/access-authn-authz/rbac/#privilege-escalation-prevention-and-bootstrapping
func CreateOrUpdateCSI(ctx context.Context, client client.Client, log logr.Logger) error {
	operatorDeployment, err := getOperatorDeployment(ctx, client, utils.GetOperatorNamespace())
	if err != nil {
		log.Error(err, "unable to get operator deployment")
		return err
	}

	err = createOrUpdateRBAC(ctx, client, operatorDeployment, log)
	if err != nil {
		log.Error(err, "unable to create/update RBAC")
		return err
	}

	err = createMonConfigMap(ctx, client, operatorDeployment, log)
	if err != nil {
		log.Error(err, "unable to create mon configmap")
		return err
	}

	err = createEncryptionConfigMap(ctx, client, operatorDeployment, log)
	if err != nil {
		log.Error(err, "unable to create encryption configmap")
		return err
	}

	desiredCephFSDeployment := templates.GetCephFSDeployment()
	actualCephFSDeployment := &appsv1.Deployment{
		ObjectMeta: desiredCephFSDeployment.ObjectMeta,
	}
	result, err := controllerutil.CreateOrUpdate(ctx, client, actualCephFSDeployment, func() error {
		actualCephFSDeployment.Spec = desiredCephFSDeployment.Spec
		return controllerutil.SetControllerReference(operatorDeployment, actualCephFSDeployment, client.Scheme())
	})
	if err != nil {
		log.Error(err, "unable to create/update cephfs deployment")
		return err
	}
	log.Info("csi cephfs deployment", "operation", result, "name", actualCephFSDeployment.Name)

	desiredCephFSDaemonSet := templates.GetCephFSDaemonSet()
	actualCephFSDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: desiredCephFSDaemonSet.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, client, actualCephFSDaemonSet, func() error {
		actualCephFSDaemonSet.Spec = desiredCephFSDaemonSet.Spec
		return controllerutil.SetControllerReference(operatorDeployment, actualCephFSDaemonSet, client.Scheme())
	})
	if err != nil {
		log.Error(err, "unable to create/update cephfs daemonset")
		return err
	}
	log.Info("csi cephfs deamonset", "operation", result, "name", actualCephFSDaemonSet.Name)

	desiredRBDDeployment := templates.GetRBDDeployment()
	actualRBDDeployment := &appsv1.Deployment{
		ObjectMeta: desiredRBDDeployment.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, client, actualRBDDeployment, func() error {
		actualRBDDeployment.Spec = desiredRBDDeployment.Spec
		return controllerutil.SetControllerReference(operatorDeployment, actualRBDDeployment, client.Scheme())
	})
	if err != nil {
		log.Error(err, "unable to create rbd deployment")
		return err
	}
	log.Info("csi rbd deployment", "operation", result, "name", actualRBDDeployment.Name)

	desiredRBDDaemonSet := templates.GetRBDDaemonSet()
	actualRBDDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: desiredRBDDaemonSet.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, client, actualRBDDaemonSet, func() error {
		actualRBDDaemonSet.Spec = desiredRBDDaemonSet.Spec
		return controllerutil.SetControllerReference(operatorDeployment, actualRBDDaemonSet, client.Scheme())
	})

	if err != nil {
		log.Error(err, "unable to create/update rbd daemonset")
		return nil
	}
	log.Info("csi rbd daemonset", "operation", result, "name", actualRBDDaemonSet.Name)
	// Need to handle deletion of the csiDriver object, we cannot set
	// ownerReference on it as its cluster scoped resource
	err = createOrUpdateCSIDriver(ctx, client, log, templates.CephFSCSIDriver)
	if err != nil {
		log.Error(err, "unable to create/update cephfs CSIDriver")
		return err
	}

	err = createOrUpdateCSIDriver(ctx, client, log, templates.RbdCSIDriver)
	if err != nil {
		log.Error(err, "unable to create/update rbd CSIDriver")
		return err
	}
	return nil
}

func getOperatorDeployment(ctx context.Context, c client.Client, namespace string) (*appsv1.Deployment, error) {
	// Fetch the operator deployment
	deployment := &appsv1.Deployment{}
	err := c.Get(ctx, types.NamespacedName{Name: "ocs-client-operator-controller-manager", Namespace: namespace}, deployment)
	if err != nil {
		return nil, err
	}

	return deployment, nil
}
