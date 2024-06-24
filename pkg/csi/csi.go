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

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/util/version"
)

const (
	sidecarsConfigPath = "/opt/config/csi-images.yaml"
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

type sidecarImages struct {
	Version         string          `yaml:"version"`
	ContainerImages containerImages `yaml:"containerImages"`
}

var SidecarImages *sidecarImages

func InitializeSidecars(log logr.Logger, ver string) error {
	// ready yaml files and yaml unmarshal to SidecarImages
	// and set to csiSidecarImages
	si := []sidecarImages{}
	yamlFile, err := os.ReadFile(sidecarsConfigPath)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(yamlFile, &si)
	if err != nil {
		return err
	}

	pltVersion := version.MustParseGeneric(ver)

	closestMinor := int64(-1)
	for idx := range si {
		siVersion := version.MustParseGeneric(si[idx].Version)
		log.Info("searching for the most compatible CSI image version", "CSI", siVersion, "Platform", pltVersion)

		// only check sidecar image versions that are not higher than platform
		if siVersion.Major() == pltVersion.Major() && siVersion.Minor() <= pltVersion.Minor() {
			// filter sidecar closest to platform version
			if int64(siVersion.Minor()) > closestMinor {
				SidecarImages = &si[idx]
				closestMinor = int64(siVersion.Minor())
			}
			if closestMinor == int64(pltVersion.Minor()) { // exact match and early exit
				break
			}
		} else {
			log.Info("skipping sidecar images: version greater than platform version")
		}
	}
	if SidecarImages == nil {
		// happens only if all sidecars image versions are greater than platform
		return fmt.Errorf("failed to find container details suitable for %v platform version", pltVersion)
	}

	log.Info("selected sidecar images", "version", SidecarImages.Version)

	return nil
}

// GetCephFSDriverName returns the cephfs driver name
func GetCephFSDriverName() string {
	return "openshift-storage.cephfs.csi.ceph.com"
}

// GetRBDDriverName returns the rbd driver name
func GetRBDDriverName() string {
	return "openshift-storage.rbd.csi.ceph.com"
}
