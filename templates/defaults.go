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
	"os"
	"strconv"

	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/kubernetes"
)

var Namespace = utils.GetOperatorNamespace()

const (
	sidecarsConfigPath = "/etc/ocs-client-operator/images.yaml"
	// kubelet directory path
	defaultKubeletDirPath        = "/var/lib/kubelet"
	defaultProvisionerSocketPath = "unix:///csi/csi-provisioner.sock"
	defaultPluginSocketPath      = "unix:///csi/csi.sock"
	defaultSocketDir             = "/csi"

	// driver name prefix
	rbdDriverSuffix    = "rbd.csi.ceph.com"
	cephFSDriverSuffix = "cephfs.csi.ceph.com"

	// configmap names
	MonConfigMapName        = "ceph-csi-configs"
	EncryptionConfigMapName = "ceph-csi-kms-config"
)

type containerImages struct {
	ProvisionerImageURL     string `yaml:"provisionerImageURL"`
	AttacherImageURL        string `yaml:"attacherImageURL"`
	ResizerImageURL         string `yaml:"resizerImageURL"`
	SnapshotterImageURL     string `yaml:"snapshotterImageURL"`
	DriverRegistrarImageURL string `yaml:"driverRegistrarImageURL"`
	CephCSIImageURL         string `yaml:"cephCSIImageURL"`
	CSIADDONSImageURL       string `yaml:"csiaddonsImageURL"`
}

type CSISidecarImages struct {
	Version         string          `yaml:"version"`
	ContainerImages containerImages `yaml:"containerImages"`
}

var csiSidecarImages = new(CSISidecarImages)

func InitializeSidecars(c *kubernetes.Clientset) error {
	// ready yaml files and yaml unmarshal to CSISidecarImages
	// and set to csiSidecarImages
	sidecarImages := []CSISidecarImages{}
	yamlFile, err := os.ReadFile(sidecarsConfigPath)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(yamlFile, &sidecarImages)
	if err != nil {
		return err
	}

	sv, err := c.DiscoveryClient.ServerVersion()
	if err != nil {
		return err
	}
	major, err := strconv.ParseUint(sv.Major, 10, 64)
	if err != nil {
		return err
	}
	minor, err := strconv.ParseUint(sv.Minor, 10, 64)
	if err != nil {
		return err
	}

	for _, image := range sidecarImages {
		v := version.MustParseGeneric(image.Version)
		if uint(major) == v.Major() && uint(minor) == v.Minor() {
			csiSidecarImages = &image
			break
		}
	}
	if csiSidecarImages.Version == "" {
		return fmt.Errorf("failed to find container details for %v version in %v", sv, sidecarImages)
	}

	return nil
}

func getCephFSDriverName() string {
	return fmt.Sprintf("%s.%s", Namespace, cephFSDriverSuffix)
}

func getRBDDriverName() string {
	return fmt.Sprintf("%s.%s", Namespace, rbdDriverSuffix)
}

func (c CSISidecarImages) getProvisionerImage() string {
	return c.ContainerImages.ProvisionerImageURL
}

func (c CSISidecarImages) getAttacherImage() string {
	return c.ContainerImages.AttacherImageURL
}

func (c CSISidecarImages) getResizerImage() string {
	return c.ContainerImages.ResizerImageURL
}

func (c CSISidecarImages) getSnapshotterImage() string {
	return c.ContainerImages.SnapshotterImageURL
}

func (c CSISidecarImages) getDriverRegistrarImage() string {
	return c.ContainerImages.DriverRegistrarImageURL
}

func (c CSISidecarImages) getCephCSIImage() string {
	return c.ContainerImages.CephCSIImageURL
}

func (c CSISidecarImages) getCSIADDONSImage() string {
	return c.ContainerImages.CSIADDONSImageURL
}
