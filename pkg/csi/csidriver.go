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
	"fmt"
	"reflect"

	v1k8scsi "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func CreateCSIDriver(ctx context.Context, client client.Client, csiDriver *v1k8scsi.CSIDriver) error {
	actualDriver := &v1k8scsi.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: csiDriver.Name,
		},
	}
	needCreation := false
	err := client.Get(ctx, types.NamespacedName{Name: csiDriver.Name}, actualDriver)
	if err == nil {
		// check if the spec is the same for the existing object and the new one
		if !reflect.DeepEqual(csiDriver.Spec, actualDriver.Spec) {
			needCreation = true
			err = client.Delete(ctx, actualDriver)
			if err != nil {
				return fmt.Errorf("error deleting CSIDriver %s: %v", csiDriver.Name, err)
			}
		}
	}

	if errors.IsNotFound(err) || needCreation {
		err = client.Create(ctx, csiDriver)
	}

	return err
}

// TODO need to check how to delete the csidriver object

//nolint:deadcode,unused
func DeleteCSIDriver(ctx context.Context, client client.Client, name string) error {
	csiDriver := &v1k8scsi.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	err := client.Delete(ctx, csiDriver)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("error deleting CSIDriver %s: %v", csiDriver.Name, err)
	}

	return nil
}
