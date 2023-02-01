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

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetOperatorDeployment returns the operator deployment object
func GetOperatorDeployment(ctx context.Context, c client.Client, namespace string) (*appsv1.Deployment, error) {
	deployment := &appsv1.Deployment{}
	err := c.Get(ctx, types.NamespacedName{Name: "ocs-client-operator-controller-manager", Namespace: namespace}, deployment)
	if err != nil {
		return nil, err
	}

	return deployment, nil
}
