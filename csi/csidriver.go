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
	"context"

	v1k8scsi "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func createOrUpdateCSIDriver(ctx context.Context, client client.Client, log klog.Logger, csiDriver *v1k8scsi.CSIDriver) error {
	actualDriver := &v1k8scsi.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: csiDriver.Name,
		},
	}
	err := client.Get(ctx, types.NamespacedName{Name: csiDriver.Name}, actualDriver)

	if errors.IsNotFound(err) {
		err = client.Create(ctx, csiDriver)
	} else if err == nil {
		// As FSGroupPolicy field is immutable, if it requires an
		// update we need to delete the CSIDriver object first and
		// requeue the reconcile request.
		if csiDriver.Spec.FSGroupPolicy != nil && actualDriver.Spec.FSGroupPolicy != nil &&
			*actualDriver.Spec.FSGroupPolicy != *csiDriver.Spec.FSGroupPolicy {
			log.Info("FSGroupPolicy mismatch, CR deletion required",
				"driverName", csiDriver.Name,
				"expected", csiDriver.Spec.FSGroupPolicy,
				"actual", actualDriver.Spec.FSGroupPolicy,
			)

			err = client.Delete(ctx, actualDriver)
			if err == nil {
				err = client.Create(ctx, csiDriver)
			}
		} else {
			// For csidriver we need to provide the resourceVersion when updating the object.
			// From the docs (https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#metadata)
			// > "This value MUST be treated as opaque by clients and passed unmodified back to the server"
			csiDriver.ObjectMeta.ResourceVersion = actualDriver.ObjectMeta.ResourceVersion
			err = client.Update(ctx, csiDriver)
		}
	}

	if err != nil {
		log.Error(err, "error creating/updating CSIDriver", "driverName", csiDriver.Name)
	}

	return err
}

// TODO need to check how to delete the csidriver object

//nolint:deadcode,unused
func deleteCSIDriver(ctx context.Context, client client.Client, log klog.Logger, name string) error {
	csiDriver := &v1k8scsi.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	err := client.Delete(ctx, csiDriver)
	if err != nil && !errors.IsNotFound(err) {
		log.Error(err, "error deleting CSIDriver", "driverName", csiDriver.Name)
		return err
	}

	return nil
}
