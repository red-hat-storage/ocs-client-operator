/*
Copyright 2024 Red Hat, Inc.
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

// this package contains the utilities that depends on controller runtime

package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// from https://github.com/operator-framework/operator-lib/blob/v0.11.x/conditions/factory.go#L73-L78,
	// the value for this env var is set by olm and it is same as the csv that we are deployed from,
	// since this var isn't exported by operator-lib, we should have a fallback function to get the csv name.
	ocsClientOperatorCSVEnvName = "OPERATOR_CONDITION_NAME"
	csvPrefix                   = "ocs-client-operator"
)

func GetOperatorVersion(ctx context.Context, c client.Client, namespace string) (string, error) {

	var version string

	csvName := os.Getenv(ocsClientOperatorCSVEnvName)
	if csvName != "" {
		csv := &opv1a1.ClusterServiceVersion{}
		csv.Name = csvName
		csv.Namespace = namespace
		if err := c.Get(ctx, types.NamespacedName{Name: csv.Name, Namespace: csv.Namespace}, csv); err != nil {
			return "", fmt.Errorf("failed to get csv %q in namespace %q, err: %v", csvName, namespace, err)
		}
		version = csv.Spec.Version.String()
	} else {
		csvList := &opv1a1.ClusterServiceVersionList{}
		if err := c.List(ctx, csvList, client.InNamespace(namespace)); err != nil {
			return "", fmt.Errorf("failed to list csv resources in namespace: %q, err: %v", namespace, err)
		}
		csv := utils.Find(csvList.Items, func(csv *opv1a1.ClusterServiceVersion) bool {
			return strings.HasPrefix(csv.Name, csvPrefix) && csv.Status.Phase != opv1a1.CSVPhasePending
		})
		if csv == nil {
			return "", fmt.Errorf("unable to find csv with prefix %q", csvPrefix)
		}
		version = csv.Spec.Version.String()
	}

	return version, nil
}

func GetPlatformVersion(ctx context.Context, c client.Client) (string, error) {
	clusterVersion := &configv1.ClusterVersion{}
	clusterVersion.Name = "version"
	if err := c.Get(ctx, types.NamespacedName{Name: clusterVersion.Name}, clusterVersion); err != nil {
		return "", fmt.Errorf("failed to get clusterVersion: %v", err)
	}
	updated := utils.Find(clusterVersion.Status.History, func(history *configv1.UpdateHistory) bool {
		return history.State == configv1.CompletedUpdate
	})
	return updated.Version, nil
}