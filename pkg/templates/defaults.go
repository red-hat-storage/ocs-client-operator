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

const (
	// kubelet directory path
	DefaultKubeletDirPath        = "/var/lib/kubelet"
	DefaultProvisionerSocketPath = "unix:///csi/csi-provisioner.sock"
	DefaultPluginSocketPath      = "unix:///csi/csi.sock"
	DefaultCSIAddonsSocketPath   = "unix:///csi/csi-addons.sock"
	DefaultSocketDir             = "/csi"
	DefaultStagingPath           = "/var/lib/kubelet/plugins/kubernetes.io/csi/"

	// configmap names
	MonConfigMapName        = "ceph-csi-configs"
	EncryptionConfigMapName = "ceph-csi-kms-config"

	// default port numbers
	DefaultCSIAddonsContainerPort = int32(9070)
)
