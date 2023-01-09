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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

var (
	cephfsDaemonsetLabels = map[string]string{
		"app": "csi-cephfsplugin",
	}

	biDirectionalMount        = corev1.MountPropagationBidirectional
	hostPathDirectoryorCreate = corev1.HostPathDirectoryOrCreate
	hostPathDirectory         = corev1.HostPathDirectory
)

func GetCephFSDaemonSet() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{

		ObjectMeta: metav1.ObjectMeta{
			Name:      "csi-cephfsplugin",
			Namespace: Namespace,
			Labels:    cephfsDaemonsetLabels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: cephfsDaemonsetLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "csi-cephfsplugin",
					Namespace: Namespace,
					Labels:    cephfsDaemonsetLabels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: CephFSPluginServiceAccountName,
					HostNetwork:        true,
					PriorityClassName:  "system-node-critical",
					Containers: append([]corev1.Container{},
						getNodeDriverRegistrarContainer(fmt.Sprintf("%s/plugins/%s/csi.sock", defaultKubeletDirPath, getCephFSDriverName())),
						v1.Container{
							Name:            "csi-cephfsplugin",
							Image:           csiSidecarImages.getCephCSIImage(),
							ImagePullPolicy: corev1.PullIfNotPresent,
							SecurityContext: &corev1.SecurityContext{
								Privileged:               pointer.Bool(true),
								AllowPrivilegeEscalation: pointer.Bool(true),
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
								fmt.Sprintf("--drivername=%s", getCephFSDriverName()),
							},
							Resources: cephFSPluginResourceRequirements,
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
									Value: defaultPluginSocketPath,
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
									MountPath: defaultSocketDir,
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
									MountPath:        fmt.Sprintf("%s/plugins/", defaultKubeletDirPath),
									MountPropagation: &biDirectionalMount,
								},
								{
									Name:             "pods-mount-dir",
									MountPath:        fmt.Sprintf("%s/pods", defaultKubeletDirPath),
									MountPropagation: &biDirectionalMount,
								},
							},
						},
					),
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
										Name: MonConfigMapName,
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
									Path: fmt.Sprintf("%s/plugins/%s", defaultKubeletDirPath, getCephFSDriverName()),
									Type: &hostPathDirectoryorCreate,
								},
							},
						},
						{
							Name: "csi-plugins-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: fmt.Sprintf("%s/plugins/", defaultKubeletDirPath),
									Type: &hostPathDirectory,
								},
							},
						},
						{
							Name: "registration-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: fmt.Sprintf("%s/plugins_registry/", defaultKubeletDirPath),
									Type: &hostPathDirectory,
								},
							},
						},
						{
							Name: "pods-mount-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: fmt.Sprintf("%s/pods", defaultKubeletDirPath),
									Type: &hostPathDirectory,
								},
							},
						},
					},
				},
			},
		},
	}
}
