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

	"github.com/red-hat-storage/ocs-client-operator/templates"

	secv1 "github.com/openshift/api/security/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func createOrUpdateRBAC(ctx context.Context, c client.Client, ownerDep *appsv1.Deployment, log klog.Logger) error {
	namespace := templates.Namespace

	ok, err := templates.IsSCCSupported(ctx, c)
	if err != nil {
		log.Error(err, "failed to check if SCC is supported")
		return err
	}
	if ok {
		desiredSCC := templates.GetSecurityContextConstraints(namespace)
		actualSCC := &secv1.SecurityContextConstraints{
			ObjectMeta: desiredSCC.ObjectMeta,
		}
		result, err := controllerutil.CreateOrUpdate(ctx, c, actualSCC, func() error {
			actualSCC = desiredSCC
			return nil
		})
		if err != nil {
			log.Error(err, "failed to create SCC")
			return err
		}
		log.Info("csi SCC", "operation", result, "name", actualSCC.Name)
	} else {
		log.Info("SCC is not supported, skipping SCC creation")
	}

	cephFSProvisionerServiceAccount := corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templates.CephFSProvisionerServiceAccountName,
			Namespace: namespace,
		},
	}
	result, err := controllerutil.CreateOrUpdate(ctx, c, &cephFSProvisionerServiceAccount, func() error {
		return controllerutil.SetControllerReference(ownerDep, &cephFSProvisionerServiceAccount, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create cephfs provisioner service account")
		return err
	}
	log.Info("csi cephfs provisioner service account", "operation", result, "name", cephFSProvisionerServiceAccount.Name)

	desiredCephFSProvisionerClusterRole := templates.CephFSProvisionerClusterRole
	actualCephFSProvisionerClusterRole := &rbacv1.ClusterRole{
		ObjectMeta: desiredCephFSProvisionerClusterRole.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, actualCephFSProvisionerClusterRole, func() error {
		actualCephFSProvisionerClusterRole.Rules = desiredCephFSProvisionerClusterRole.Rules
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create cephfs provisioner clusterrole")
		return err
	}
	log.Info("csi cephfs provisioner cluster role", "operation", result, "name", actualCephFSProvisionerClusterRole.Name)

	cephFSProvisionerClusterRoleBinding := templates.CephFSProvisionerClusterRoleBinding
	cephFSProvisionerClusterRoleBinding.Subjects[0].Namespace = namespace
	result, err = controllerutil.CreateOrUpdate(ctx, c, &cephFSProvisionerClusterRoleBinding, func() error {
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create cephfs provisioner clusterrolebinding")
		return err
	}
	log.Info("csi cephfs provisioner cluster role binding", "operation", result, "name", cephFSProvisionerClusterRoleBinding.Name)

	desiredcephFSProvisionerRole := templates.CephFSProvisionerRole
	desiredcephFSProvisionerRole.Namespace = namespace
	actualcephFSProvisionerRole := &rbacv1.Role{
		ObjectMeta: desiredcephFSProvisionerRole.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, actualcephFSProvisionerRole, func() error {
		actualcephFSProvisionerRole.Rules = desiredcephFSProvisionerRole.Rules
		return controllerutil.SetControllerReference(ownerDep, actualcephFSProvisionerRole, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create cephfs provisioner role")
		return err
	}
	log.Info("csi cephfs provisioner role", "operation", result, "name", actualcephFSProvisionerRole.Name)

	cephFSProvisionerRoleBinding := templates.CephFSProvisionerRoleBinding
	cephFSProvisionerRoleBinding.Namespace = namespace
	cephFSProvisionerRoleBinding.Subjects[0].Namespace = namespace
	result, err = controllerutil.CreateOrUpdate(ctx, c, &cephFSProvisionerRoleBinding, func() error {
		return controllerutil.SetControllerReference(ownerDep, &cephFSProvisionerRoleBinding, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create cephfs provisioner rolebinding")
		return err
	}
	log.Info("csi cephfs provisioner role binding", "operation", result, "name", cephFSProvisionerRoleBinding.Name)

	cephFSPluginServiceAccount := corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templates.CephFSPluginServiceAccountName,
			Namespace: namespace,
		},
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, &cephFSPluginServiceAccount, func() error {
		return controllerutil.SetControllerReference(ownerDep, &cephFSPluginServiceAccount, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create cephfs plugin service account")
		return err
	}
	log.Info("csi cephfs plugin service account", "operation", result, "name", cephFSPluginServiceAccount.Name)

	desiredCephFSPluginClusterRole := templates.CephFSPluginClusterRole
	actualCephFSPluginClusterRole := &rbacv1.ClusterRole{
		ObjectMeta: desiredCephFSPluginClusterRole.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, actualCephFSPluginClusterRole, func() error {
		actualCephFSPluginClusterRole.Rules = desiredCephFSPluginClusterRole.Rules
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create cephfs plugin clusterrole")
		return err
	}
	log.Info("csi cephfs plugin cluster role", "operation", result, "name", actualCephFSPluginClusterRole.Name)

	cephFSPluginClusterRoleBinding := templates.CephFSPluginClusterRoleBinding
	cephFSPluginClusterRoleBinding.Subjects[0].Namespace = namespace
	result, err = controllerutil.CreateOrUpdate(ctx, c, &cephFSPluginClusterRoleBinding, func() error {
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create cephfs plugin clusterrolebinding")
		return err
	}
	log.Info("csi cephfs plugin cluster role binding", "operation", result, "name", cephFSPluginClusterRoleBinding.Name)

	rbdProvisionerServiceAccount := corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templates.RBDProvisionerServiceAccountName,
			Namespace: namespace,
		},
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, &rbdProvisionerServiceAccount, func() error {
		return controllerutil.SetControllerReference(ownerDep, &rbdProvisionerServiceAccount, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create rbd provisioner service account")
		return err
	}
	log.Info("csi rbd provisioner service account", "operation", result, "name", rbdProvisionerServiceAccount.Name)

	desiredRBDProvisionerClusterRole := templates.RBDProvisionerClusterRole
	actualRBDProvisionerClusterRole := &rbacv1.ClusterRole{
		ObjectMeta: desiredRBDProvisionerClusterRole.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, actualRBDProvisionerClusterRole, func() error {
		actualRBDProvisionerClusterRole.Rules = desiredRBDProvisionerClusterRole.Rules
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create rbd provisioner clusterrole")
		return err
	}
	log.Info("csi rbd provisioner cluster role", "operation", result, "name", templates.RBDProvisionerClusterRole.Name)

	rbdProvisionerClusterRoleBinding := templates.RBDProvisionerClusterRoleBinding
	rbdProvisionerClusterRoleBinding.Subjects[0].Namespace = namespace
	result, err = controllerutil.CreateOrUpdate(ctx, c, &rbdProvisionerClusterRoleBinding, func() error {
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create rbd provisioner clusterrolebinding")
		return err
	}
	log.Info("csi rbd provisioner cluster role binding", "operation", result, "name", rbdProvisionerClusterRoleBinding.Name)

	desriedRBDProvisionerRole := templates.RBDProvisionerRole
	desriedRBDProvisionerRole.Namespace = namespace
	actualRBDProvisionerRole := &rbacv1.Role{
		ObjectMeta: desriedRBDProvisionerRole.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, actualRBDProvisionerRole, func() error {
		actualRBDProvisionerRole.Rules = desriedRBDProvisionerRole.Rules
		return controllerutil.SetControllerReference(ownerDep, actualRBDProvisionerRole, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create rbd provisioner role")
		return err
	}
	log.Info("csi rbd provisioner role", "operation", result, "name", actualRBDProvisionerRole.Name)

	rbdProvisionerRoleBinding := templates.RBDProvisionerRoleBinding
	rbdProvisionerRoleBinding.Namespace = namespace
	rbdProvisionerRoleBinding.Subjects[0].Namespace = namespace
	result, err = controllerutil.CreateOrUpdate(ctx, c, &rbdProvisionerRoleBinding, func() error {
		return controllerutil.SetControllerReference(ownerDep, &rbdProvisionerRoleBinding, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create rbd provisioner rolebinding")
		return err
	}
	log.Info("csi rbd provisioner role binding", "operation", result, "name", rbdProvisionerRoleBinding.Name)

	rbdPluginServiceAccount := corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templates.RBDPluginServiceAccountName,
			Namespace: namespace,
		},
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, &rbdPluginServiceAccount, func() error {
		return controllerutil.SetControllerReference(ownerDep, &rbdPluginServiceAccount, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create rbd plugin service account")
		return err
	}
	log.Info("csi rbd plugin service account", "operation", result, "name", rbdPluginServiceAccount.Name)

	desiredRBDPluginClusterRole := templates.RBDPluginClusterRole
	actualRBDPluginClusterRole := &rbacv1.ClusterRole{
		ObjectMeta: desiredRBDPluginClusterRole.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, actualRBDPluginClusterRole, func() error {
		actualRBDPluginClusterRole.Rules = desiredRBDPluginClusterRole.Rules
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create rbd plugin clusterrole")
		return err
	}
	log.Info("csi rbd plugin cluster role", "operation", result, "name", actualRBDPluginClusterRole.Name)

	rbdPluginClusterRoleBinding := templates.RBDPluginClusterRoleBinding
	rbdPluginClusterRoleBinding.Subjects[0].Namespace = namespace
	result, err = controllerutil.CreateOrUpdate(ctx, c, &rbdPluginClusterRoleBinding, func() error {
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create rbd plugin clusterrolebinding")
		return err
	}
	log.Info("csi rbd plugin cluster role binding", "operation", result, "name", rbdPluginClusterRoleBinding.Name)

	desiredRBDPluginRole := templates.RBDPluginRole
	desiredRBDPluginRole.Namespace = namespace
	actualRBDPluginRole := &rbacv1.Role{
		ObjectMeta: desiredRBDPluginRole.ObjectMeta,
	}
	result, err = controllerutil.CreateOrUpdate(ctx, c, actualRBDPluginRole, func() error {
		actualRBDPluginRole.Rules = desiredRBDPluginRole.Rules
		return controllerutil.SetControllerReference(ownerDep, actualRBDPluginRole, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create rbd plugin role")
		return err
	}
	log.Info("csi rbd plugin role", "operation", result, "name", actualRBDPluginRole.Name)

	rbdPluginRoleBinding := templates.RBDPluginRoleBinding
	rbdPluginRoleBinding.Namespace = namespace
	rbdPluginRoleBinding.Subjects[0].Namespace = namespace
	result, err = controllerutil.CreateOrUpdate(ctx, c, &rbdPluginRoleBinding, func() error {
		return controllerutil.SetControllerReference(ownerDep, &rbdPluginRoleBinding, c.Scheme())
	})
	if err != nil {
		log.Error(err, "failed to create rbd plugin rolebinding")
		return err
	}
	log.Info("csi rbd plugin role binding", "operation", result, "name", rbdPluginRoleBinding.Name)

	return nil
}
