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
	"context"
	"fmt"

	v1k8scsi "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (r *ClusterVersionReconciler) ensureCsiDriverCr(
	ctx context.Context,
	driverName string,
	fsGroupPolicy v1k8scsi.FSGroupPolicy,
	attachRequired, mountInfo bool,
) error {
	csiDriver := &v1k8scsi.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: driverName,
		},
		Spec: v1k8scsi.CSIDriverSpec{
			AttachRequired: &attachRequired,
			PodInfoOnMount: &mountInfo,
		},
	}
	csiDriver.Spec.FSGroupPolicy = &fsGroupPolicy

	actualDriver := &v1k8scsi.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: csiDriver.Name,
		},
	}

	err := r.Client.Get(ctx, types.NamespacedName{Name: csiDriver.Name}, actualDriver)

	if errors.IsNotFound(err) {
		err = r.Client.Create(ctx, csiDriver)
	} else if err == nil {
		// As FSGroupPolicy field is immutable, if it requires an
		// update we need to delete the CSIDriver object first and
		// requeue the reconcile request.
		if csiDriver.Spec.FSGroupPolicy != nil && actualDriver.Spec.FSGroupPolicy != nil &&
			*actualDriver.Spec.FSGroupPolicy != *csiDriver.Spec.FSGroupPolicy {
			r.Log.Info("FSGroupPolicy mismatch, CR deletion required",
				"driverName", csiDriver.Name,
				"expected", csiDriver.Spec.FSGroupPolicy,
				"actual", actualDriver.Spec.FSGroupPolicy,
			)

			err = r.Client.Delete(ctx, actualDriver)
			if err == nil {
				err = fmt.Errorf("deleted existing CSIDriver, requeueing")
			}
		}
	}

	if err != nil {
		r.Log.Error(err, "error ensuring CSIDriver", "driverName", csiDriver.Name)
		return err
	}

	return nil
}
