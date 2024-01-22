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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

var (
	rbdDaemonsetLabels = map[string]string{
		"app": "csi-rbdplugin",
	}
)

const (
	RBDDaemonSetName = "csi-rbdplugin"
)

func GetRBDDaemonSet(namespace string) *appsv1.DaemonSet {
	driverRegistrar := templates.DriverRegistrar.DeepCopy()
	driverRegistrar.Image = sidecarImages.ContainerImages.DriverRegistrarImageURL
	driverRegistrar.Args = append(
		driverRegistrar.Args,
		fmt.Sprintf("--kubelet-registration-path=%s/plugins/%s/csi.sock",
			templates.DefaultKubeletDirPath,
			GetRBDDriverName()),
	)
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RBDDaemonSetName,
			Namespace: namespace,
			Labels:    rbdDaemonsetLabels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: rbdDaemonsetLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      RBDDaemonSetName,
					Namespace: namespace,
					Labels:    rbdDaemonsetLabels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: rbdPluginServiceAccountName,
					HostNetwork:        true,
					HostPID:            true,
					PriorityClassName:  "system-node-critical",
					Containers: append([]corev1.Container{},
						*driverRegistrar,
						v1.Container{
							Name:            "csi-rbdplugin",
							Image:           sidecarImages.ContainerImages.CephCSIImageURL,
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
								"--type=rbd",
								"--nodeserver=true",
								fmt.Sprintf("--drivername=%s", GetRBDDriverName()),
								fmt.Sprintf("--stagingpath=%s/plugins/kubernetes.io/csi/", templates.DefaultKubeletDirPath),
								"--csi-addons-endpoint=$(CSIADDONS_ENDPOINT)",
							},
							Resources: templates.RBDPluginResourceRequirements,
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
								{
									Name:  "CSIADDONS_ENDPOINT",
									Value: "unix:///csi/csi-addons.sock",
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
						v1.Container{
							Name:            "csi-addons",
							Image:           sidecarImages.ContainerImages.CSIADDONSImageURL,
							ImagePullPolicy: corev1.PullIfNotPresent,
							SecurityContext: &corev1.SecurityContext{
								Privileged:               ptr.To(true),
								AllowPrivilegeEscalation: ptr.To(true),
							},
							Args: []string{
								"--node-id=$(NODE_ID)",
								"--v=5",
								"--pod=$(POD_NAME)",
								"--namespace=$(POD_NAMESPACE)",
								"--pod-uid=$(POD_UID)",
								fmt.Sprintf("--stagingpath=%s/plugins/kubernetes.io/csi/", templates.DefaultKubeletDirPath),
								"--csi-addons-address=$(CSIADDONS_ENDPOINT)",
								"--controller-port=9070",
							},
							Ports: []v1.ContainerPort{
								{
									ContainerPort: 9070,
								},
							},
							Env: []corev1.EnvVar{
								{
									Name: "POD_UID",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.uid",
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
									Name: "POD_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.name",
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
									Name:  "CSIADDONS_ENDPOINT",
									Value: "unix:///csi/csi-addons.sock",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "plugin-dir",
									MountPath: templates.DefaultSocketDir,
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
									Path: fmt.Sprintf("%s/plugins/%s", templates.DefaultKubeletDirPath, GetRBDDriverName()),
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
							Name: "oidc-token", VolumeSource: corev1.VolumeSource{
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
					Tolerations: []corev1.Toleration{
						utils.GetTolerationForCSIPods(),
					},
				},
			},
		},
	}
}
