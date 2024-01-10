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
	"fmt"
	"os"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// return csv matching the prefix or else error
func GetCSVByPrefix(ctx context.Context, c client.Client, prefix, namespace string) (*opv1a1.ClusterServiceVersion, error) {
	csvList := &opv1a1.ClusterServiceVersionList{}
	if err := c.List(ctx, csvList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list csv resources in namespace: %q, err: %v", namespace, err)
	}
	csv := Find(csvList.Items, func(csv *opv1a1.ClusterServiceVersion) bool {
		return strings.HasPrefix(csv.Name, prefix) && csv.Status.Phase != opv1a1.CSVPhasePending
	})
	if csv == nil {
		return nil, fmt.Errorf("unable to find csv with prefix %q", prefix)
	}
	return csv, nil
}

func GetPlatformVersion(ctx context.Context, c client.Client) (string, error) {
	clusterVersion := &configv1.ClusterVersion{}
	clusterVersion.Name = "version"
	if err := c.Get(ctx, types.NamespacedName{Name: clusterVersion.Name}, clusterVersion); err != nil {
		return "", fmt.Errorf("failed to get clusterVersion: %v", err)
	}
	updated := Find(clusterVersion.Status.History, func(history *configv1.UpdateHistory) bool {
		return history.State == configv1.CompletedUpdate
	})
	return updated.Version, nil
}
