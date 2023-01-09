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

package templates

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	// clusterrole and clusterolebinding names
	cephFSProvisionerClusterRoleName        = "cephfs-csi-provisioner-clusterrole"
	cephFSProvisionerClusterRoleBindingName = "cephfs-csi-provisioner-clusterrolebinding"
	cephFSPluginClusterRoleName             = "cephfs-csi-plugin-clusterrole"
	cephFSPluginClusterRoleBindingName      = "cephfs-csi-plugin-clusterrolebinding"
	rbdProvisionerClusterRoleName           = "rbd-csi-provisioner-clusterrole"
	rbdProvisionerClusterRoleBindingName    = "rbd-csi-provisioner-clusterrolebinding"
	rbdPluginClusterRoleName                = "rbd-csi-plugin-clusterrole"
	rbdPluginClusterRoleBindingName         = "rbd-csi-plugin-clusterrolebinding"

	// Roles
	cephFSProvisionerRoleName        = "cephfs-csi-provisioner-role"
	cephFSProvisionerRoleBindingName = "cephfs-csi-provisioner-rolebinding"
	rbdProvisionerRoleName           = "rbd-csi-provisioner-role"
	rbdProvisionerRoleBindingName    = "rbd-csi-provisioner-rolebinding"
	rbdPluginRoleName                = "rbd-csi-plugin-role"
	rbdPluginRoleBindingName         = "rbd-csi-plugin-rolebinding"

	// serviceaccount names
	CephFSProvisionerServiceAccountName = "cephfs-csi-provisioner-sa"
	CephFSPluginServiceAccountName      = "cephfs-csi-plugin-sa"
	RBDProvisionerServiceAccountName    = "rbd-csi-provisioner-sa"
	RBDPluginServiceAccountName         = "rbd-csi-plugin-sa"
)

var CephFSProvisionerClusterRole = rbacv1.ClusterRole{
	ObjectMeta: metav1.ObjectMeta{
		Name: cephFSProvisionerClusterRoleName,
	},
	// RBAC details for the provisioner https://github.com/kubernetes-csi/external-provisioner/blob/master/deploy/kubernetes/rbac.yaml
	Rules: []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumes"},
			Verbs:     []string{"get", "list", "watch", "create", "delete", "update", "patch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumeclaims"},
			Verbs:     []string{"get", "list", "watch", "update"},
		},
		{
			APIGroups: []string{"storage.k8s.io"},
			Resources: []string{"storageclasses"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"secrets"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshots"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshotcontents"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{"storage.k8s.io"},
			Resources: []string{"csinodes"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"nodes"},
			Verbs:     []string{"get", "list", "watch"},
		},

		// RBAC details for the resizer
		// https://github.com/kubernetes-csi/external-resizer/blob/master/deploy/kubernetes/rbac.yaml
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumes/status"},
			Verbs:     []string{"patch"},
		},

		// RBAC details for the attacher
		// https://github.com/kubernetes-csi/external-attacher/blob/master/deploy/kubernetes/rbac.yaml
		{
			APIGroups: []string{"storage.k8s.io"},
			Resources: []string{"volumeattachments"},
			Verbs:     []string{"get", "list", "watch", "patch"},
		},
		{
			APIGroups: []string{"storage.k8s.io"},
			Resources: []string{"volumeattachments/status"},
			Verbs:     []string{"update", "patch"},
		},
		// RBAC details for the snapshotter
		// https://github.com/kubernetes-csi/external-snapshotter/blob/master/deploy/kubernetes/csi-snapshotter/rbac-csi-snapshotter.yaml
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshotclasses"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshotcontents"},
			Verbs:     []string{"create", "get", "list", "watch", "update"},
		},
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshotcontents/status"},
			Verbs:     []string{"update", "patch"},
		},
	},
}

var CephFSProvisionerClusterRoleBinding = rbacv1.ClusterRoleBinding{
	ObjectMeta: metav1.ObjectMeta{
		Name: cephFSProvisionerClusterRoleBindingName,
	},
	RoleRef: rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "ClusterRole",
		Name:     cephFSProvisionerClusterRoleName,
	},
	Subjects: []rbacv1.Subject{
		{
			Kind: "ServiceAccount",
			Name: CephFSProvisionerServiceAccountName,
		},
	},
}

var CephFSPluginClusterRole = rbacv1.ClusterRole{
	ObjectMeta: metav1.ObjectMeta{
		Name: cephFSPluginClusterRoleName,
	},
	Rules: []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"nodes"},
			Verbs:     []string{"get", "list", "watch"},
		},
	},
}

var CephFSPluginClusterRoleBinding = rbacv1.ClusterRoleBinding{
	ObjectMeta: metav1.ObjectMeta{
		Name: cephFSPluginClusterRoleBindingName,
	},
	RoleRef: rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "ClusterRole",
		Name:     cephFSPluginClusterRoleName,
	},
	Subjects: []rbacv1.Subject{
		{
			Kind: "ServiceAccount",
			Name: CephFSPluginServiceAccountName,
		},
	},
}

var CephFSProvisionerRole = rbacv1.Role{
	ObjectMeta: metav1.ObjectMeta{
		Name: cephFSProvisionerRoleName,
	},
	Rules: []rbacv1.PolicyRule{
		{
			APIGroups: []string{"coordination.k8s.io"},
			Resources: []string{"leases"},
			Verbs:     []string{"get", "watch", "list", "delete", "update", "create"},
		},
	},
}

var CephFSProvisionerRoleBinding = rbacv1.RoleBinding{
	ObjectMeta: metav1.ObjectMeta{
		Name: cephFSProvisionerRoleBindingName,
	},
	RoleRef: rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "Role",
		Name:     cephFSProvisionerRoleName,
	},
	Subjects: []rbacv1.Subject{
		{
			Kind: "ServiceAccount",
			Name: CephFSProvisionerServiceAccountName,
		},
	},
}

var RBDProvisionerClusterRole = rbacv1.ClusterRole{
	ObjectMeta: metav1.ObjectMeta{
		Name: rbdProvisionerClusterRoleName,
	},
	// RBAC details for the provisioner https://github.com/kubernetes-csi/external-provisioner/blob/master/deploy/kubernetes/rbac.yaml
	Rules: []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumes"},
			Verbs:     []string{"get", "list", "watch", "create", "delete", "update", "patch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumeclaims"},
			Verbs:     []string{"get", "list", "watch", "update"},
		},
		{
			APIGroups: []string{"storage.k8s.io"},
			Resources: []string{"storageclasses"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"secrets"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshots"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshotcontents"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{"storage.k8s.io"},
			Resources: []string{"csinodes"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"nodes"},
			Verbs:     []string{"get", "list", "watch"},
		},

		// RBAC details for the resizer
		// https://github.com/kubernetes-csi/external-resizer/blob/master/deploy/kubernetes/rbac.yaml
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumes/status"},
			Verbs:     []string{"patch"},
		},

		// RBAC details for the attacher
		// https://github.com/kubernetes-csi/external-attacher/blob/master/deploy/kubernetes/rbac.yaml
		{
			APIGroups: []string{"storage.k8s.io"},
			Resources: []string{"volumeattachments"},
			Verbs:     []string{"get", "list", "watch", "patch"},
		},
		{
			APIGroups: []string{"storage.k8s.io"},
			Resources: []string{"volumeattachments/status"},
			Verbs:     []string{"update", "patch"},
		},
		// RBAC details for the snapshotter
		// https://github.com/kubernetes-csi/external-snapshotter/blob/master/deploy/kubernetes/csi-snapshotter/rbac-csi-snapshotter.yaml
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshotclasses"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshotcontents"},
			Verbs:     []string{"create", "get", "list", "watch", "update"},
		},
		{
			APIGroups: []string{"snapshot.storage.k8s.io"},
			Resources: []string{"volumesnapshotcontents/status"},
			Verbs:     []string{"update", "patch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"serviceaccounts"},
			Verbs:     []string{"get"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"serviceaccounts/token"},
			Verbs:     []string{"create"},
		},
	},
}

var RBDProvisionerClusterRoleBinding = rbacv1.ClusterRoleBinding{
	ObjectMeta: metav1.ObjectMeta{
		Name: rbdProvisionerClusterRoleBindingName,
	},
	RoleRef: rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "ClusterRole",
		Name:     rbdProvisionerClusterRoleName,
	},
	Subjects: []rbacv1.Subject{
		{
			Kind: "ServiceAccount",
			Name: RBDProvisionerServiceAccountName,
		},
	},
}

var RBDPluginClusterRole = rbacv1.ClusterRole{
	ObjectMeta: metav1.ObjectMeta{
		Name: rbdPluginClusterRoleName,
	},
	Rules: []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"nodes"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"csiaddons.openshift.io"},
			Resources: []string{"csiaddonsnodes"},
			Verbs:     []string{"create"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"secrets"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumes"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{"storage.k8s.io"},
			Resources: []string{"volumeattachments"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get", "list"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"serviceaccounts"},
			Verbs:     []string{"get"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"serviceaccounts/token"},
			Verbs:     []string{"create"},
		},
	},
}

var RBDPluginClusterRoleBinding = rbacv1.ClusterRoleBinding{
	ObjectMeta: metav1.ObjectMeta{
		Name: rbdPluginClusterRoleBindingName,
	},
	RoleRef: rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "ClusterRole",
		Name:     rbdPluginClusterRoleName,
	},
	Subjects: []rbacv1.Subject{
		{
			Kind: "ServiceAccount",
			Name: RBDPluginServiceAccountName,
		},
	},
}

var RBDProvisionerRole = rbacv1.Role{
	ObjectMeta: metav1.ObjectMeta{
		Name: rbdProvisionerRoleName,
	},
	Rules: []rbacv1.PolicyRule{
		{
			APIGroups: []string{"coordination.k8s.io"},
			Resources: []string{"leases"},
			Verbs:     []string{"get", "watch", "list", "delete", "update", "create"},
		},
		{
			APIGroups: []string{"csiaddons.openshift.io"},
			Resources: []string{"csiaddonsnodes"},
			Verbs:     []string{"create"},
		},
	},
}

var RBDProvisionerRoleBinding = rbacv1.RoleBinding{
	ObjectMeta: metav1.ObjectMeta{
		Name: rbdProvisionerRoleBindingName,
	},
	RoleRef: rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "Role",
		Name:     rbdProvisionerRoleName,
	},
	Subjects: []rbacv1.Subject{
		{
			Kind: "ServiceAccount",
			Name: RBDProvisionerServiceAccountName,
		},
	},
}

var RBDPluginRole = rbacv1.Role{
	ObjectMeta: metav1.ObjectMeta{
		Name: rbdPluginRoleName,
	},
	Rules: []rbacv1.PolicyRule{
		{
			APIGroups: []string{"coordination.k8s.io"},
			Resources: []string{"leases"},
			Verbs:     []string{"get", "watch", "list", "delete", "update", "create"},
		},
		{
			APIGroups: []string{"csiaddons.openshift.io"},
			Resources: []string{"csiaddonsnodes"},
			Verbs:     []string{"create"},
		},
	},
}

var RBDPluginRoleBinding = rbacv1.RoleBinding{
	ObjectMeta: metav1.ObjectMeta{
		Name: rbdPluginRoleBindingName,
	},
	RoleRef: rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "Role",
		Name:     rbdPluginRoleName,
	},
	Subjects: []rbacv1.Subject{
		{
			Kind: "ServiceAccount",
			Name: RBDPluginServiceAccountName,
		},
	},
}
