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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/pointer"
)

func getProvisionerContainer() corev1.Container {
	return corev1.Container{
		Name:            "csi-provisioner",
		Image:           csiSidecarImages.getProvisionerImage(),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			fmt.Sprintf("--csi-address=%s", defaultProvisionerSocketPath),
			"--v=5",
			"--timeout=150s",
			"--retry-interval-start=500ms",
			"--leader-election=true",
			fmt.Sprintf("--leader-election-namespace=%s", Namespace),
			"--default-fstype=ext4",
			"--extra-create-metadata=true",
		},
		Env: []corev1.EnvVar{},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "socket-dir",
				MountPath: defaultSocketDir,
			},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				"memory": resource.MustParse("85Mi"),
				"cpu":    resource.MustParse("15m"),
			},
			Limits: corev1.ResourceList{
				"memory": resource.MustParse("85Mi"),
				"cpu":    resource.MustParse("15m"),
			},
		},
	}
}

func getResizerContainer() corev1.Container {
	return corev1.Container{
		Name:            "csi-resizer",
		Image:           csiSidecarImages.getResizerImage(),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			fmt.Sprintf("--csi-address=%s", defaultProvisionerSocketPath),
			"--v=5",
			"--timeout=150s",
			"--leader-election=true",
			fmt.Sprintf("--leader-election-namespace=%s", Namespace),
			"--handle-volume-inuse-error=false",
		},
		Env: []corev1.EnvVar{},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "socket-dir",
				MountPath: defaultSocketDir,
			},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				"memory": resource.MustParse("55Mi"),
				"cpu":    resource.MustParse("10m"),
			},
			Limits: corev1.ResourceList{
				"memory": resource.MustParse("55Mi"),
				"cpu":    resource.MustParse("10m"),
			},
		},
	}
}

func getAttacherContainer() corev1.Container {
	return corev1.Container{
		Name:            "csi-attacher",
		Image:           csiSidecarImages.getAttacherImage(),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			fmt.Sprintf("--csi-address=%s", defaultProvisionerSocketPath),
			"--v=5",
			"--timeout=150s",
			"--leader-election=true",
			fmt.Sprintf("--leader-election-namespace=%s", Namespace),
		},
		Env: []corev1.EnvVar{},

		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "socket-dir",
				MountPath: defaultSocketDir,
			},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				"memory": resource.MustParse("45Mi"),
				"cpu":    resource.MustParse("10m"),
			},
			Limits: corev1.ResourceList{
				"memory": resource.MustParse("45Mi"),
				"cpu":    resource.MustParse("10m"),
			},
		},
	}
}

func getSnapshotterContainer() corev1.Container {
	return corev1.Container{
		Name:            "csi-snapshotter",
		Image:           csiSidecarImages.getSnapshotterImage(),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			fmt.Sprintf("--csi-address=%s", defaultProvisionerSocketPath),
			"--v=5",
			"--timeout=150s",
			"--leader-election=true",
			fmt.Sprintf("--leader-election-namespace=%s", Namespace),
			"--extra-create-metadata=true",
		},
		Env: []corev1.EnvVar{},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "socket-dir",
				MountPath: defaultSocketDir,
			},
		},

		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				"memory": resource.MustParse("35Mi"),
				"cpu":    resource.MustParse("10m"),
			},
			Limits: corev1.ResourceList{
				"memory": resource.MustParse("35Mi"),
				"cpu":    resource.MustParse("10m"),
			},
		},
	}
}

func getNodeDriverRegistrarContainer(registrationPath string) corev1.Container {
	return corev1.Container{
		Name:            "csi-driver-registrar",
		Image:           csiSidecarImages.getDriverRegistrarImage(),
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			Privileged:               pointer.BoolPtr(true),
			AllowPrivilegeEscalation: pointer.Bool(true),
		},
		Args: []string{
			fmt.Sprintf("--csi-address=%s", defaultPluginSocketPath),
			"--v=5",
			fmt.Sprintf("--kubelet-registration-path=%s", registrationPath),
		},

		Env: []corev1.EnvVar{
			{
				Name: "KUBE_NODE_NAME",
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
				Name:      "registration-dir",
				MountPath: "/registration",
			},
		},

		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				"memory": resource.MustParse("25Mi"),
				"cpu":    resource.MustParse("10m"),
			},
			Limits: corev1.ResourceList{
				"memory": resource.MustParse("25Mi"),
				"cpu":    resource.MustParse("10m"),
			},
		},
	}
}

var rbdPluginResourceRequirements = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{
		"memory": resource.MustParse("270Mi"),
		"cpu":    resource.MustParse("25m"),
	},
	Limits: corev1.ResourceList{
		"memory": resource.MustParse("270Mi"),
		"cpu":    resource.MustParse("25m"),
	},
}

var cephFSPluginResourceRequirements = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{
		"memory": resource.MustParse("160Mi"),
		"cpu":    resource.MustParse("20m"),
	},
	Limits: corev1.ResourceList{
		"memory": resource.MustParse("160Mi"),
		"cpu":    resource.MustParse("20m"),
	},
}
