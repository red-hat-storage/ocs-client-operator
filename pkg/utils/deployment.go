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
	"os"
	"strings"

	apiv2 "github.com/operator-framework/api/pkg/operators/v2"
	"github.com/operator-framework/operator-lib/conditions"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetOperatorDeployment returns the operator deployment object
func GetOperatorDeployment(ctx context.Context, c client.Client) (*appsv1.Deployment, error) {
	deployment := &appsv1.Deployment{}
	podNameStrings := strings.Split(os.Getenv(OperatorPodNameEnvVar), "-")
	deploymentName := strings.Join(podNameStrings[:len(podNameStrings)-2], "-")
	err := c.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: GetOperatorNamespace()}, deployment)
	if err != nil {
		return nil, err
	}

	return deployment, nil
}

// NewUpgradeable returns a Conditions interface to Get or Set OperatorConditions
func NewUpgradeable(cl client.Client) (conditions.Condition, error) {
	return conditions.InClusterFactory{Client: cl}.NewCondition(apiv2.ConditionType(apiv2.Upgradeable))
}
