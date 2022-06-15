/*
Copyright 2022 Red Hat OpenShift Data Foundation.
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

package controllers

import (
	v1k8scsi "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type csiDriver struct {
	name           string
	fsGroupPolicy  v1k8scsi.FSGroupPolicy
	attachRequired bool
	mountInfo      bool
}

func (d csiDriver) getCSIDriver() *v1k8scsi.CSIDriver {
	// Get CSIDriver object
	csiDriver := &v1k8scsi.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: d.name,
		},
		Spec: v1k8scsi.CSIDriverSpec{
			AttachRequired: &d.attachRequired,
			PodInfoOnMount: &d.mountInfo,
		},
	}
	csiDriver.Spec.FSGroupPolicy = &d.fsGroupPolicy
	return csiDriver
}

func shouldDelete(driver, csiDriver *v1k8scsi.CSIDriver) bool {
	// As FSGroupPolicy field is immutable, should be set only during create time.
	// if the request is to change the FSGroupPolicy, we need to delete the CSIDriver object and create it.
	if driver.Spec.FSGroupPolicy != nil && csiDriver.Spec.FSGroupPolicy != nil && *driver.Spec.FSGroupPolicy != *csiDriver.Spec.FSGroupPolicy {
		return true
	}
	return false
}
