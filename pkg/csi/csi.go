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
	"os"

	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/version"
)

var (
	// fetch the operator deployment and store it.
	OperatorDeployment = &appsv1.Deployment{}
)

const (
	sidecarsConfigPath = "/etc/ocs-client-operator/images.yaml"
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

type SidecarImages struct {
	Version         string          `yaml:"version"`
	ContainerImages containerImages `yaml:"containerImages"`
}

var sidecarImages = new(SidecarImages)

func InitializeSidecars(ver string) error {
	// ready yaml files and yaml unmarshal to SidecarImages
	// and set to csiSidecarImages
	si := []SidecarImages{}
	yamlFile, err := os.ReadFile(sidecarsConfigPath)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(yamlFile, &si)
	if err != nil {
		return err
	}

	sv := version.MustParseGeneric(ver)

	for _, image := range si {
		v := version.MustParseGeneric(image.Version)
		if sv.Major() == v.Major() && sv.Minor() == v.Minor() {
			sidecarImages = &image
			break
		}
	}
	if sidecarImages.Version == "" {
		return fmt.Errorf("failed to find container details for %v version in %v", ver, sidecarImages)
	}

	return nil
}

// GetCephFSDriverName returns the cephfs driver name
func GetCephFSDriverName(namespace string) string {
	return fmt.Sprintf("%s.cephfs.csi.ceph.com", namespace)
}

// GetRBDDriverName returns the rbd driver name
func GetRBDDriverName(namespace string) string {
	return fmt.Sprintf("%s.rbd.csi.ceph.com", namespace)
}
