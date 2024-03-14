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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	CephFSDeploymentName = "csi-cephfsplugin-provisioner"

	cephFSDeploymentContainerName = "csi-cephfsplugin"
)

var cephfsDeploymentLabels = map[string]string{
	"app": "csi-cephfsplugin-provisioner",
}

var cephFSDeploymentSpec = appsv1.DeploymentSpec{
	Replicas: ptr.To(int32(2)),
	Selector: &metav1.LabelSelector{
		MatchLabels: cephfsDeploymentLabels,
	},
	Template: corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			ServiceAccountName: cephFSProvisionerServiceAccountName,
			Containers: []corev1.Container{
				{Name: templates.ProvisionerContainer.Name},
				{Name: templates.AttacherContainer.Name},
				{Name: templates.ResizerContainer.Name},
				{Name: templates.SnapshotterContainer.Name},
				{
					Name:            cephFSDeploymentContainerName,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Args: []string{
						"--nodeid=$(NODE_ID)",
						"--endpoint=$(CSI_ENDPOINT)",
						"--v=5",
						"--pidlimit=-1",
						"--type=cephfs",
						"--controllerserver=true",
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
					},
				},
			},
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
							Items: []corev1.KeyToPath{
								{
									Key:  "config.json",
									Path: "config.json",
								},
							},
						},
					},
				},
			},
		},
	},
}

func SetCephFSDeploymentDesiredState(deploy *appsv1.Deployment) {
	// Copy required labels
	for key := range cephfsDaemonsetLabels {
		deploy.Labels[key] = cephfsDaemonsetLabels[key]
	}

	// Update the deployment set with desired spec
	cephFSDeploymentSpec.DeepCopyInto(&deploy.Spec)

	// Find and Update placeholder containers with desired state
	leaderElectionArg := fmt.Sprintf("--leader-election-namespace=%s", deploy.Namespace)
	for i := range deploy.Spec.Template.Spec.Containers {
		c := &deploy.Spec.Template.Spec.Containers[i]

		switch c.Name {
		case templates.ProvisionerContainer.Name:
			templates.ProvisionerContainer.DeepCopyInto(c)
			c.Image = sidecarImages.ContainerImages.ProvisionerImageURL
			c.Args = append(c.Args, leaderElectionArg)

		case templates.AttacherContainer.Name:
			templates.AttacherContainer.DeepCopyInto(c)
			c.Image = sidecarImages.ContainerImages.AttacherImageURL
			c.Args = append(c.Args, leaderElectionArg)

		case templates.ResizerContainer.Name:
			templates.ResizerContainer.DeepCopyInto(c)
			c.Image = sidecarImages.ContainerImages.ResizerImageURL
			c.Args = append(c.Args, leaderElectionArg)

		case templates.SnapshotterContainer.Name:
			templates.SnapshotterContainer.DeepCopyInto(c)
			c.Image = sidecarImages.ContainerImages.SnapshotterImageURL
			c.Args = append(c.Args, leaderElectionArg)

		case cephFSDeploymentContainerName:
			c.Image = sidecarImages.ContainerImages.CephCSIImageURL
		}
	}

}
