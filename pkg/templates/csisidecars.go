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
	"k8s.io/utils/ptr"
)

var ProvisionerContainer = &corev1.Container{
	Name:            "csi-provisioner",
	ImagePullPolicy: corev1.PullIfNotPresent,
	Args: []string{
		fmt.Sprintf("--csi-address=%s", DefaultProvisionerSocketPath),
		"--v=5",
		"--timeout=150s",
		"--retry-interval-start=500ms",
		"--leader-election=true",
		"--default-fstype=ext4",
		"--extra-create-metadata=true",
	},
	Env: []corev1.EnvVar{},
	VolumeMounts: []corev1.VolumeMount{
		{
			Name:      "socket-dir",
			MountPath: DefaultSocketDir,
		},
	},
}

var ResizerContainer = &corev1.Container{
	Name:            "csi-resizer",
	ImagePullPolicy: corev1.PullIfNotPresent,
	Args: []string{
		fmt.Sprintf("--csi-address=%s", DefaultProvisionerSocketPath),
		"--v=5",
		"--timeout=150s",
		"--leader-election=true",
		"--handle-volume-inuse-error=false",
	},
	Env: []corev1.EnvVar{},
	VolumeMounts: []corev1.VolumeMount{
		{
			Name:      "socket-dir",
			MountPath: DefaultSocketDir,
		},
	},
}

var AttacherContainer = &corev1.Container{
	Name:            "csi-attacher",
	ImagePullPolicy: corev1.PullIfNotPresent,
	Args: []string{
		fmt.Sprintf("--csi-address=%s", DefaultProvisionerSocketPath),
		"--v=5",
		"--timeout=150s",
		"--leader-election=true",
	},
	Env: []corev1.EnvVar{},

	VolumeMounts: []corev1.VolumeMount{
		{
			Name:      "socket-dir",
			MountPath: DefaultSocketDir,
		},
	},
}

var SnapshotterContainer = &corev1.Container{
	Name:            "csi-snapshotter",
	ImagePullPolicy: corev1.PullIfNotPresent,
	Args: []string{
		fmt.Sprintf("--csi-address=%s", DefaultProvisionerSocketPath),
		"--v=5",
		"--timeout=150s",
		"--leader-election=true",
		"--extra-create-metadata=true",
	},
	Env: []corev1.EnvVar{},
	VolumeMounts: []corev1.VolumeMount{
		{
			Name:      "socket-dir",
			MountPath: DefaultSocketDir,
		},
	},
}

var DriverRegistrar = &corev1.Container{
	Name:            "csi-driver-registrar",
	ImagePullPolicy: corev1.PullIfNotPresent,
	SecurityContext: &corev1.SecurityContext{
		Privileged:               ptr.To(true),
		AllowPrivilegeEscalation: ptr.To(true),
	},
	Args: []string{
		fmt.Sprintf("--csi-address=%s", DefaultPluginSocketPath),
		"--v=5",
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
			MountPath: DefaultSocketDir,
		},
		{
			Name:      "registration-dir",
			MountPath: "/registration",
		},
	},
}
