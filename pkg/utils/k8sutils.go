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
	"fmt"
	"maps"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OperatorNamespaceEnvVar is the constant for env variable OPERATOR_NAMESPACE
// which is the namespace where operator pod is deployed.
const OperatorNamespaceEnvVar = "OPERATOR_NAMESPACE"

// OperatorPodNameEnvVar is the constant for env variable OPERATOR_POD_NAME
const OperatorPodNameEnvVar = "OPERATOR_POD_NAME"

// StorageClientNameEnvVar is the constant for env variable STORAGE_CLIENT_NAME
const StorageClientNameEnvVar = "STORAGE_CLIENT_NAME"

const StatusReporterImageEnvVar = "STATUS_REPORTER_IMAGE"

// Value corresponding to annotation key has subscription channel
const DesiredSubscriptionChannelAnnotationKey = "ocs.openshift.io/subscription.channel"

const runCSIDaemonsetOnMaster = "RUN_CSI_DAEMONSET_ON_MASTER"

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
