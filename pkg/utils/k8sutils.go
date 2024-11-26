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

package utils

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// OperatorNamespaceEnvVar is the constant for env variable OPERATOR_NAMESPACE
	// which is the namespace where operator pod is deployed.
	OperatorNamespaceEnvVar = "OPERATOR_NAMESPACE"

	// OperatorPodNameEnvVar is the constant for env variable OPERATOR_POD_NAME
	OperatorPodNameEnvVar = "OPERATOR_POD_NAME"

	// StorageClientNameEnvVar is the constant for env variable STORAGE_CLIENT_NAME
	StorageClientNameEnvVar = "STORAGE_CLIENT_NAME"

	StatusReporterImageEnvVar = "STATUS_REPORTER_IMAGE"

	// Value corresponding to annotation key has subscription channel
	DesiredSubscriptionChannelAnnotationKey = "ocs.openshift.io/subscription.channel"

	// Value corresponding to annotation key has desired client hash
	DesiredConfigHashAnnotationKey = "ocs.openshift.io/provider-side-state"

	CronScheduleWeekly = "@weekly"

	ExitCodeThatShouldRestartTheProcess = 42

	OcsClientTimeout = 10 * time.Second
)

// GetOperatorNamespace returns the namespace where the operator is deployed.
func GetOperatorNamespace() string {
	return os.Getenv(OperatorNamespaceEnvVar)
}

func ValidateOperatorNamespace() error {
	ns := GetOperatorNamespace()
	if ns == "" {
		return fmt.Errorf("namespace not found for operator")
	}

	return nil
}

func ValidateStausReporterImage() error {
	image := os.Getenv(StatusReporterImageEnvVar)
	if image == "" {
		return fmt.Errorf("status reporter image not found")
	}

	return nil
}

// AddLabels adds values from newLabels to the keys on the supplied obj or overwrites values for existing keys on the obj
func AddLabels(obj metav1.Object, newLabels map[string]string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}
	maps.Copy(labels, newLabels)
}

// AddAnnotation adds label to a resource metadata, returns true if added else false
func AddLabel(obj metav1.Object, key string, value string) bool {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}
	if oldValue, exist := labels[key]; !exist || oldValue != value {
		labels[key] = value
		return true
	}
	return false
}

func AddAnnotations(obj metav1.Object, newAnnotations map[string]string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
		obj.SetAnnotations(annotations)
	}
	maps.Copy(annotations, newAnnotations)
}

// AddAnnotation adds an annotation to a resource metadata, returns true if added else false
func AddAnnotation(obj metav1.Object, key string, value string) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
		obj.SetAnnotations(annotations)
	}
	if oldValue, exist := annotations[key]; !exist || oldValue != value {
		annotations[key] = value
		return true
	}
	return false
}

func GetMD5Hash(text string) string {
	hash := md5.Sum([]byte(text))
	return hex.EncodeToString(hash[:])
}

func CreateOrReplace(ctx context.Context, c client.Client, obj client.Object, f controllerutil.MutateFn) error {
	key := client.ObjectKeyFromObject(obj)
	if err := c.Get(ctx, key, obj); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		if err := mutate(f, key, obj); err != nil {
			return err
		}
		if err := c.Create(ctx, obj); err != nil {
			return err
		}
		return nil
	}

	existing := obj.DeepCopyObject()
	if err := mutate(f, key, obj); err != nil {
		return err
	}

	if reflect.DeepEqual(existing, obj) {
		return nil
	}

	if err := c.Delete(ctx, obj); err != nil {
		return err
	}

	// k8s doesn't allow us to create objects when resourceVersion is set, as we are DeepCopying the
	// object, the resource version also gets copied, hence we need to set it to empty before creating it
	obj.SetResourceVersion("")
	if err := c.Create(ctx, obj); err != nil {
		return err
	}
	return nil
}

// mutate wraps a MutateFn and applies validation to its result.
func mutate(f controllerutil.MutateFn, key client.ObjectKey, obj client.Object) error {
	if err := f(); err != nil {
		return err
	}
	if newKey := client.ObjectKeyFromObject(obj); key != newKey {
		return fmt.Errorf("MutateFn cannot mutate object name and/or object namespace")
	}
	return nil
}

func GetClusterResourceQuotaName(name string) string {
	return fmt.Sprintf("storage-client-%s-resourceqouta", GetMD5Hash(name))
}
