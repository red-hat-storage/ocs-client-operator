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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

var rbdDeploymentLabels = map[string]string{
	"app": "csi-rbdplugin-provisioner",
}

const (
	RBDDeploymentName = "csi-rbdplugin-provisioner"
)

func GetRBDDeployment(namespace string) *appsv1.Deployment {
	provisioner := templates.ProvisionerContainer.DeepCopy()
	provisioner.Args = append(provisioner.Args,
		fmt.Sprintf("--leader-election-namespace=%s", namespace),
	)
	provisioner.Image = sidecarImages.ContainerImages.ProvisionerImageURL

	attacher := templates.AttacherContainer.DeepCopy()
	attacher.Args = append(attacher.Args,
		fmt.Sprintf("--leader-election-namespace=%s", namespace),
	)
	attacher.Image = sidecarImages.ContainerImages.AttacherImageURL

	resizer := templates.ResizerContainer.DeepCopy()
	resizer.Args = append(resizer.Args,
		fmt.Sprintf("--leader-election-namespace=%s", namespace),
	)
	resizer.Image = sidecarImages.ContainerImages.ResizerImageURL

	snapshotter := templates.SnapshotterContainer.DeepCopy()
	snapshotter.Args = append(snapshotter.Args,
		fmt.Sprintf("--leader-election-namespace=%s", namespace),
	)
	snapshotter.Image = sidecarImages.ContainerImages.SnapshotterImageURL

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RBDDeploymentName,
			Namespace: namespace,
			Labels:    rbdDeploymentLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(2)),
			Selector: &metav1.LabelSelector{
				MatchLabels: rbdDeploymentLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      RBDDeploymentName,
					Namespace: namespace,
					Labels:    rbdDeploymentLabels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: rbdProvisionerServiceAccountName,
					Containers: append([]corev1.Container{},
						*provisioner,
						*attacher,
						*resizer,
						*snapshotter,
						v1.Container{
							Name:            "csi-rbdplugin",
							Image:           sidecarImages.ContainerImages.CephCSIImageURL,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Args: []string{
								"--nodeid=$(NODE_ID)",
								"--endpoint=$(CSI_ENDPOINT)",
								"--v=5",
								"--pidlimit=-1",
								"--type=rbd",
								"--controllerserver=true",
								fmt.Sprintf("--drivername=%s", GetRBDDriverName()),
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
									Value: templates.DefaultProvisionerSocketPath,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "socket-dir",
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
									Name:      "ceph-csi-kms-config",
									MountPath: "/etc/ceph-csi-encryption-kms-config/",
									ReadOnly:  true,
								},
								{
									Name:      "oidc-token",
									MountPath: "/run/secrets/tokens",
									ReadOnly:  true,
								},
							},
						},
					),

					PriorityClassName: "system-cluster-critical",
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
							Name: "socket-dir",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{
									Medium: corev1.StorageMediumMemory,
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
								},
							},
						},
						{
							Name: "ceph-csi-kms-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: templates.EncryptionConfigMapName,
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
							Name: "oidc-token",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Path:              "oidc-token",
												ExpirationSeconds: ptr.To(int64(3600)),
												Audience:          "ceph-csi-kms",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
