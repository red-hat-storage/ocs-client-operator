/*
Copyright 2022 Red Hat OpenShift Data Foundation.
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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

var (
	// image names
	defaultCSIPluginImage   = "quay.io/cephcsi/cephcsi:v3.6.2"
	defaultRegistrarImage   = "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.5.1"
	defaultProvisionerImage = "registry.k8s.io/sig-storage/csi-provisioner:v3.1.0"
	defaultAttacherImage    = "registry.k8s.io/sig-storage/csi-attacher:v3.4.0"
	defaultSnapshotterImage = "registry.k8s.io/sig-storage/csi-snapshotter:v6.0.1"
	defaultResizerImage     = "registry.k8s.io/sig-storage/csi-resizer:v1.4.0"
)

const (
	// kubelet directory path
	defaultKubeletDirPath = "/var/lib/kubelet"
	defaultSocketPath     = "unix:///csi/csi-provisioner.sock"
	defaultSockerDir      = "/csi"

	// driver name prefix
	rbdDriverSuffix    = "rbd.csi.ceph.com"
	cephFSDriverSuffix = "cephfs.csi.ceph.com"

	// configmap names
	encryptionConfigMapName = "ceph-csi-kms-config"
	monConfigMapName        = "ceph-csi-configs"
)

func getCephFSDriverName(namespace string) string {
	return fmt.Sprintf("%s.%s", namespace, cephFSDriverSuffix)
}

func getRBDDriverName(namespace string) string {
	return fmt.Sprintf("%s.%s", namespace, rbdDriverSuffix)
}

func getRBDControllerDeployment(namespace, clusterVersion string) *appsv1.Deployment {
	// RBD CSI Controller Deployment
	name := "csi-rbdplugin-provisioner"
	var replicas int32 = 2
	volumes := []corev1.Volume{
		{Name: "host-dev", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev"}}},
		{Name: "host-sys", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/sys"}}},
		{Name: "lib-modules", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/lib/modules/"}}},
		{Name: "socket-dir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
		{Name: "keys-tmp-dir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
		{Name: "ceph-csi-configs", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: monConfigMapName}}}},
		{Name: "ceph-csi-kms-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: encryptionConfigMapName}}}},
		{Name: "oidc-token", VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{
					{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Path:              "oidc-token",
							ExpirationSeconds: pointer.Int64Ptr(3600),
							Audience:          "ceph-csi-kms",
						},
					},
				},
			},
		},
		},
	}

	labels := map[string]string{
		"app": "csi-rbdplugin-provisioner",
	}
	s := sideCarContainer{
		namespace:      namespace,
		clusterVersion: clusterVersion,
	}

	// get all containers that are part of csi controller deployment
	containers := []corev1.Container{
		*s.getCsiProvisionerContainer(),
		*s.getCsiResizerContainer(),
		*s.getCsiSnapshotterContainer(),
		*s.getCsiAttacherContainer(),
		*s.getCephCsiContainer("rbd", getRBDDriverName(namespace), true),
		// not includuing below sidecar yet as they are not default
		// liveness probe
		// omap generator
		// csi-addon sidecar
		// volume replication
	}

	controllerDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels:    labels,
				},
				Spec: corev1.PodSpec{
					Containers:         containers,
					ServiceAccountName: "csi-rbd-provisioner-sa",
					PriorityClassName:  "system-cluster-critical",
					Volumes:            volumes,
				},
			},
		},
	}

	return controllerDeployment
}

func getCephFSControllerDeployment(namespace, clusterVersion string) *appsv1.Deployment {
	// CephFS CSI Controller Deployment
	name := "csi-cephfsplugin-provisioner"
	var replicas int32 = 2
	volumes := []corev1.Volume{
		{Name: "host-dev", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev"}}},
		{Name: "host-sys", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/sys"}}},
		{Name: "lib-modules", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/lib/modules"}}},
		{Name: "socket-dir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
		{Name: "keys-tmp-dir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
		{Name: "ceph-csi-configs", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: monConfigMapName}}}},
		// No support for custom ceph.conf yet
	}

	labels := map[string]string{
		"app": "csi-cephfsplugin-provisioner",
	}
	s := sideCarContainer{
		namespace:      namespace,
		clusterVersion: clusterVersion,
	}
	// get all containers that are part of csi controller deployment
	containers := []corev1.Container{
		*s.getCsiProvisionerContainer(),
		*s.getCsiResizerContainer(),
		*s.getCsiSnapshotterContainer(),
		*s.getCsiAttacherContainer(),
		*s.getCephCsiContainer("cephfs", getCephFSDriverName(namespace), true),
		// not includuing below sidecar yet as they are not default
		// liveness probe
	}

	controllerDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels:    labels,
				},
				Spec: corev1.PodSpec{
					Containers:         containers,
					ServiceAccountName: "csi-cephfs-provisioner-sa",
					PriorityClassName:  "system-cluster-critical",
					Volumes:            volumes,
				},
			},
		},
	}

	return controllerDeployment
}

func getCephFSDaemonSet(namespace, clusterVersion string) *appsv1.DaemonSet {
	// CephFS Plugin DeamonSet
	name := "csi-cephfsplugin"
	driverName := getCephFSDriverName(namespace)
	pluginPath := fmt.Sprintf("%s/plugins/%s", defaultKubeletDirPath, driverName)
	hostPathDirectoryorCreate := corev1.HostPathDirectoryOrCreate
	hostPathDirectory := corev1.HostPathDirectory
	volumes := []corev1.Volume{
		{Name: "host-dev", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev"}}},
		{Name: "host-sys", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/sys"}}},
		{Name: "lib-modules", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/lib/modules"}}},
		{Name: "host-run-mount", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/run/mount"}}},
		{Name: "socket-dir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
		{Name: "keys-tmp-dir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},

		{Name: "ceph-csi-configs", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: monConfigMapName}}}},
		{Name: "plugin-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: pluginPath, Type: &hostPathDirectoryorCreate}}},
		{Name: "csi-plugins-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: fmt.Sprintf("%s/plugins/", defaultKubeletDirPath), Type: &hostPathDirectory}}},
		{Name: "registration-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: fmt.Sprintf("%s/plugins_registry/", defaultKubeletDirPath), Type: &hostPathDirectory}}},
		{Name: "pods-mount-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: fmt.Sprintf("%s/pods", defaultKubeletDirPath), Type: &hostPathDirectory}}},

		// No support for custom ceph.conf yet
	}

	labels := map[string]string{
		"app": name,
	}

	s := sideCarContainer{
		namespace:      namespace,
		clusterVersion: clusterVersion,
	}

	cepfsPluginContainer := s.getCephCsiContainer("cephfs", driverName, false)
	// set security context for cephfs plugin which is only required when
	// running as daemonset
	cepfsPluginContainer.SecurityContext = &corev1.SecurityContext{
		Privileged: pointer.BoolPtr(true),
		Capabilities: &corev1.Capabilities{
			Add: []corev1.Capability{"SYS_ADMIN"},
		},
		AllowPrivilegeEscalation: pointer.BoolPtr(true),
	}
	// get all containers that are part of csi controller deployment
	containers := []corev1.Container{
		*s.getDriverRegistrarContainer(fmt.Sprintf("%s/csi.sock", pluginPath)),
		*cepfsPluginContainer,
		// not includuing below sidecar yet as they are not default
		// liveness probe
	}

	pluginDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels:    labels,
				},
				Spec: corev1.PodSpec{
					HostNetwork:        true, // Need to run with host networking for map/mount operation
					HostPID:            true,
					DNSPolicy:          corev1.DNSClusterFirstWithHostNet,
					Containers:         containers,
					ServiceAccountName: "csi-cephfs-plugin-sa",
					PriorityClassName:  "system-node-critical",
					Volumes:            volumes,
				},
			},
		},
	}

	return pluginDaemonSet

}

func getRBDDaemonSet(namespace, clusterVersion string) *appsv1.DaemonSet {
	// CephFS Plugin DeamonSet
	name := "csi-rbdplugin"
	driverName := getRBDDriverName(namespace)
	pluginPath := fmt.Sprintf("%s/plugins/%s", defaultKubeletDirPath, driverName)
	hostPathDirectoryorCreate := corev1.HostPathDirectoryOrCreate
	hostPathDirectory := corev1.HostPathDirectory
	volumes := []corev1.Volume{
		{Name: "host-dev", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev"}}},
		{Name: "host-sys", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/sys"}}},
		{Name: "lib-modules", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/lib/modules"}}},
		{Name: "host-run-mount", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/run/mount"}}},
		{Name: "socket-dir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
		{Name: "keys-tmp-dir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
		{Name: "ceph-csi-configs", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: monConfigMapName}}}},
		{Name: "ceph-csi-kms-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: encryptionConfigMapName}}}},
		{Name: "plugin-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: pluginPath, Type: &hostPathDirectoryorCreate}}},
		{Name: "csi-plugins-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: fmt.Sprintf("%s/plugins/", defaultKubeletDirPath), Type: &hostPathDirectory}}},
		{Name: "registration-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: fmt.Sprintf("%s/plugins_registry/", defaultKubeletDirPath), Type: &hostPathDirectory}}},
		{Name: "pods-mount-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: fmt.Sprintf("%s/pods", defaultKubeletDirPath), Type: &hostPathDirectory}}},
		{Name: "oidc-token", VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{
					{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Path:              "oidc-token",
							ExpirationSeconds: pointer.Int64Ptr(3600),
							Audience:          "ceph-csi-kms",
						},
					},
				},
			},
		}},
		// No support for custom ceph.conf yet
	}

	labels := map[string]string{
		"app": name,
	}
	s := sideCarContainer{
		namespace:      namespace,
		clusterVersion: clusterVersion,
	}
	rbdPluginContainer := s.getCephCsiContainer("rbd", driverName, false)
	// set security context for cephfs plugin which is only required when
	// running as daemonset
	rbdPluginContainer.SecurityContext = &corev1.SecurityContext{
		Privileged: pointer.BoolPtr(true),
		Capabilities: &corev1.Capabilities{
			Add: []corev1.Capability{"SYS_ADMIN"},
		},
		AllowPrivilegeEscalation: pointer.BoolPtr(true),
	}
	// get all containers that are part of csi controller deployment
	containers := []corev1.Container{
		*s.getDriverRegistrarContainer(fmt.Sprintf("%s/csi.sock", pluginPath)),
		*rbdPluginContainer,
		// not includuing below sidecar yet as they are not default
		// liveness probe
	}

	pluginDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels:    labels,
				},
				Spec: corev1.PodSpec{
					HostNetwork:        true, // Need to run with host networking for map/mount operation
					HostPID:            true,
					DNSPolicy:          corev1.DNSClusterFirstWithHostNet,
					Containers:         containers,
					ServiceAccountName: "csi-rbd-plugin-sa",
					PriorityClassName:  "system-node-critical",
					Volumes:            volumes,
				},
			},
		},
	}

	return pluginDaemonSet

}

type sideCarContainer struct {
	namespace      string
	clusterVersion string
}

func (s *sideCarContainer) getCsiProvisionerContainer() *corev1.Container {
	// csi provisioner container
	args := []string{
		fmt.Sprintf("--csi-address=%s", defaultSocketPath),
		"--v=5",
		"--timeout=150s",
		"--retry-interval-start=500ms",
		"--leader-election=true",
		fmt.Sprintf("--leader-election-namespace=%s", s.namespace),
		"--default-fstype=ext4",
		"--extra-create-metadata=true",
	}

	resourceRequirements := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			// Empty for now
		},
		Requests: corev1.ResourceList{
			// Empty for now
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "socket-dir", MountPath: defaultSockerDir},
	}

	env := []corev1.EnvVar{
		// Empty for now
	}

	csiProvisioner := &corev1.Container{
		Name:         "csi-provisioner",
		Image:        defaultProvisionerImage,
		Args:         args,
		Resources:    resourceRequirements,
		VolumeMounts: volumeMounts,
		Env:          env,
	}

	return csiProvisioner
}

func (s *sideCarContainer) getCsiResizerContainer() *corev1.Container {
	// csi resizer container
	args := []string{
		fmt.Sprintf("--csi-address=%s", defaultSocketPath),
		"--v=5",
		"--timeout=150s",
		"--leader-election=true",
		fmt.Sprintf("--leader-election-namespace=%s", s.namespace),
		"--handle-volume-inuse-error=false",
	}

	resourceRequirements := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			// Empty for now
		},
		Requests: corev1.ResourceList{
			// Empty for now
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "socket-dir", MountPath: defaultSockerDir},
	}

	env := []corev1.EnvVar{
		// Empty for now
	}

	csiResizer := &corev1.Container{
		Name:            "csi-resizer",
		Image:           defaultResizerImage,
		Args:            args,
		Resources:       resourceRequirements,
		VolumeMounts:    volumeMounts,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             env,
	}

	return csiResizer
}

func (s *sideCarContainer) getCsiAttacherContainer() *corev1.Container {
	// csi attacher container
	args := []string{
		fmt.Sprintf("--csi-address=%s", defaultSocketPath),
		"--v=5",
		"--timeout=150s",
		"--leader-election=true",
		fmt.Sprintf("--leader-election-namespace=%s", s.namespace),
	}

	resourceRequirements := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			// Empty for now
		},
		Requests: corev1.ResourceList{
			// Empty for now
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "socket-dir", MountPath: defaultSockerDir},
	}

	env := []corev1.EnvVar{
		// Empty for now
	}

	csiAttacher := &corev1.Container{
		Name:            "csi-attacher",
		Image:           defaultAttacherImage,
		Args:            args,
		Resources:       resourceRequirements,
		VolumeMounts:    volumeMounts,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             env,
	}

	return csiAttacher
}

func (s *sideCarContainer) getCsiSnapshotterContainer() *corev1.Container {
	// csi snapshotter container
	args := []string{
		fmt.Sprintf("--csi-address=%s", defaultSocketPath),
		"--v=5",
		"--timeout=150s",
		"--leader-election=true",
		fmt.Sprintf("--leader-election-namespace=%s", s.namespace),
		"--extra-create-metadata=true",
	}

	resourceRequirements := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			// Empty for now
		},
		Requests: corev1.ResourceList{
			// Empty for now
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "socket-dir", MountPath: defaultSockerDir},
	}

	env := []corev1.EnvVar{
		// Empty for now
	}

	csiSnapshotter := &corev1.Container{
		Name:            "csi-snaphotter",
		Image:           defaultSnapshotterImage,
		Args:            args,
		Resources:       resourceRequirements,
		VolumeMounts:    volumeMounts,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             env,
	}

	return csiSnapshotter
}

func (s *sideCarContainer) getCephCsiContainer(pluginType, driverName string, controller bool) *corev1.Container {
	// csi plugin container
	args := []string{
		"--nodeid=$(NODE_ID)",
		"--endpoint=$(CSI_ENDPOINT)",
		"--v=5",
		"--pidlimit=-1",
		fmt.Sprintf("--type=%s", pluginType),
		fmt.Sprintf("--controllerserver=%t", controller),
		fmt.Sprintf("--drivername=%s", driverName),
	}

	resourceRequirements := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			// Empty for now
		},
		Requests: corev1.ResourceList{
			// Empty for now
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "socket-dir", MountPath: defaultSockerDir},
		{Name: "host-dev", MountPath: "/dev"},
		{Name: "host-sys", MountPath: "/sys"},
		{Name: "lib-modules", MountPath: "/lib/modules", ReadOnly: true},
		{Name: "ceph-csi-configs", MountPath: "/etc/ceph-csi-config"},
		{Name: "keys-tmp-dir", MountPath: "/tmp/csi/keys"},
	}

	if !controller {
		biDirectionalMount := corev1.MountPropagationBidirectional
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "host-run-mount", MountPath: "/run/mount"})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "csi-plugins-dir", MountPath: fmt.Sprintf("%s/plugins/", defaultKubeletDirPath), MountPropagation: &biDirectionalMount})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "pods-mount-dir", MountPath: fmt.Sprintf("%s/pods", defaultKubeletDirPath), MountPropagation: &biDirectionalMount})
	}
	// encryption is only supported for rbd not for cephfs
	if pluginType == "rbd" {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "ceph-csi-kms-config", MountPath: "/etc/ceph-csi-encryption-kms-config/", ReadOnly: true})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "oidc-token", MountPath: "/run/secrets/tokens", ReadOnly: true})
	}

	env := []corev1.EnvVar{
		{
			Name: "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.podIP",
				},
			},
		},
		{
			Name: "NODE_ID",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "spec.nodeName",
				},
			},
		},
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		{
			Name:  "CSI_ENDPOINT",
			Value: defaultSocketPath,
		},
	}

	cephCsiPlugin := &corev1.Container{
		Name:            fmt.Sprintf("csi-%splugin", pluginType),
		Image:           defaultCSIPluginImage,
		Args:            args,
		Resources:       resourceRequirements,
		VolumeMounts:    volumeMounts,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             env,
	}

	return cephCsiPlugin
}

func (s *sideCarContainer) getDriverRegistrarContainer(registrationPath string) *corev1.Container {
	// csi driver-registrar container
	args := []string{
		fmt.Sprintf("--csi-address=%s", defaultSocketPath),
		"--v=5",
		fmt.Sprintf("--kubelet-registration-path=%s", registrationPath),
	}

	resourceRequirements := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			// Empty for now
		},
		Requests: corev1.ResourceList{
			// Empty for now
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "plugin-dir", MountPath: defaultSockerDir},
		{Name: "registration-dir", MountPath: "/registration"},
	}

	env := []corev1.EnvVar{
		{
			Name: "KUBE_NODE_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "spec.nodeName",
				},
			},
		},
	}

	csiDriverRegistrar := &corev1.Container{
		Name:  "csi-driver-registrar",
		Image: defaultRegistrarImage,
		SecurityContext: &corev1.SecurityContext{
			Privileged: pointer.BoolPtr(true),
		},
		Args:         args,
		Resources:    resourceRequirements,
		VolumeMounts: volumeMounts,
		Env:          env,
	}

	return csiDriverRegistrar
}
