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
	"os"
)

// OperatorNamespaceEnvVar is the constant for env variable OPERATOR_NAMESPACE
// which is the namespace where operator pod is deployed.
const OperatorNamespaceEnvVar = "OPERATOR_NAMESPACE"

// OperatorPodNameEnvVar is the constant for env variable OPERATOR_POD_NAME
const OperatorPodNameEnvVar = "OPERATOR_POD_NAME"

// StorageClientNameEnvVar is the constant for env variable STORAGE_CLIENT_NAME
const StorageClientNameEnvVar = "STORAGE_CLIENT_NAME"

// StorageClientNamespaceEnvVar is the constant for env variable STORAGE_CLIENT_NAMESPACE
const StorageClientNamespaceEnvVar = "STORAGE_CLIENT_NAMESPACE"

const StatusReporterImageEnvVar = "STATUS_REPORTER_IMAGE"

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
