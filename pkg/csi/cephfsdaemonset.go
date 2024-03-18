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
	"fmt"

	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	CephFSDaemonSetName = "csi-cephfsplugin"

	cephFSDaemonSetContainerName = "csi-cephfsplugin"
)

var (
	cephfsDaemonsetLabels = map[string]string{
		"app": "csi-cephfsplugin",
	}

	biDirectionalMount        = corev1.MountPropagationBidirectional
	hostPathDirectoryorCreate = corev1.HostPathDirectoryOrCreate
	hostPathDirectory         = corev1.HostPathDirectory
)

var cephFSDaemonSetSpec = appsv1.DaemonSetSpec{
	Selector: &metav1.LabelSelector{
		MatchLabels: cephfsDaemonsetLabels,
	},
	Template: corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: cephfsDaemonsetLabels,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: cephFSPluginServiceAccountName,
			HostNetwork:        true,
			PriorityClassName:  "system-node-critical",
			Containers: []corev1.Container{
				{Name: templates.DriverRegistrar.Name},
				{
					Name:            cephFSDaemonSetContainerName,
					ImagePullPolicy: corev1.PullIfNotPresent,
					SecurityContext: &corev1.SecurityContext{
						Privileged:               ptr.To(true),
						AllowPrivilegeEscalation: ptr.To(true),
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{
								"SYS_ADMIN",
							},
						},
					},
					Args: []string{
						"--nodeid=$(NODE_ID)",
						"--endpoint=$(CSI_ENDPOINT)",
						"--v=5",
						"--pidlimit=-1",
						"--type=cephfs",
						"--nodeserver=true",
						fmt.Sprintf("--drivername=%s", GetCephFSDriverName()),
					},
					Env: []corev1.EnvVar{
						{
							Name: "POD_IP",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "status.podIP",
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
							Value: templates.DefaultPluginSocketPath,
						},
						{
							Name: "NODE_ID",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "spec.nodeName",
								},
							},
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "plugin-dir",
							MountPath: templates.DefaultSocketDir,
						},
						{
							Name:      "host-dev",
							MountPath: "/dev",
						},
						{
							Name:      "host-sys",
							MountPath: "/sys",
						},
						{
							Name:      "lib-modules",
							MountPath: "/lib/modules",
							ReadOnly:  true,
						},
						{
							Name:      "ceph-csi-configs",
							MountPath: "/etc/ceph-csi-config",
						},
						{
							Name:      "keys-tmp-dir",
							MountPath: "/tmp/csi/keys",
						},
						{
							Name:      "host-run-mount",
							MountPath: "/run/mount",
						},
						{
							Name:             "csi-plugins-dir",
							MountPath:        fmt.Sprintf("%s/plugins/", templates.DefaultKubeletDirPath),
							MountPropagation: &biDirectionalMount,
						},
						{
							Name:             "pods-mount-dir",
							MountPath:        fmt.Sprintf("%s/pods", templates.DefaultKubeletDirPath),
							MountPropagation: &biDirectionalMount,
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "host-dev",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/dev",
						},
					},
				},
				{
					Name: "host-sys",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/sys",
						},
					},
				},
				{
					Name: "lib-modules",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/lib/modules",
						},
					},
				},
				{
					Name: "host-run-mount",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/run/mount",
						},
					},
				},
				{
					Name: "keys-tmp-dir",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium: corev1.StorageMediumMemory,
						},
					},
				},
				{
					Name: "ceph-csi-configs",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: templates.MonConfigMapName,
							},
							Items: []corev1.KeyToPath{
								{
									Key:  "config.json",
									Path: "config.json",
								},
							},
						},
					},
				},
				{
					Name: "plugin-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: fmt.Sprintf("%s/plugins/%s", templates.DefaultKubeletDirPath, GetCephFSDriverName()),
							Type: &hostPathDirectoryorCreate,
						},
					},
				},
				{
					Name: "csi-plugins-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: fmt.Sprintf("%s/plugins/", templates.DefaultKubeletDirPath),
							Type: &hostPathDirectory,
						},
					},
				},
				{
					Name: "registration-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: fmt.Sprintf("%s/plugins_registry/", templates.DefaultKubeletDirPath),
							Type: &hostPathDirectory,
						},
					},
				},
				{
					Name: "pods-mount-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: fmt.Sprintf("%s/pods", templates.DefaultKubeletDirPath),
							Type: &hostPathDirectory,
						},
					},
				},
			},
			Tolerations: []corev1.Toleration{
				utils.GetTolerationForCSIPods(),
			},
		},
	},
}

func SetCephFSDaemonSetDesiredState(ds *appsv1.DaemonSet) {
	// Copy required labels
	utils.AddLabels(ds, cephfsDaemonsetLabels)

	// Update the demaon set with desired spec
	cephFSDaemonSetSpec.DeepCopyInto(&ds.Spec)

	// Update containers spec with desired state
	for i := range ds.Spec.Template.Spec.Containers {
		c := &ds.Spec.Template.Spec.Containers[i]
		switch c.Name {
		case templates.DriverRegistrar.Name:
			templates.DriverRegistrar.DeepCopyInto(c)
			c.Image = sidecarImages.ContainerImages.DriverRegistrarImageURL
			c.Args = append(c.Args, fmt.Sprintf(
				"--kubelet-registration-path=%s/plugins/%s/csi.sock",
				templates.DefaultKubeletDirPath,
				GetCephFSDriverName(),
			))

		case cephFSDaemonSetContainerName:
			c.Image = sidecarImages.ContainerImages.CephCSIImageURL
		}
	}
}
