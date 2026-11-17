package controller

import (
	apiv2 "github.com/operator-framework/api/pkg/operators/v2"
	"github.com/operator-framework/operator-lib/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewUpgradeable returns a Conditions interface to Get or Set the OLM
// "Upgradeable" OperatorCondition for this operator.
func NewUpgradeable(cl client.Client) (conditions.Condition, error) {
	return conditions.InClusterFactory{Client: cl}.NewCondition(apiv2.ConditionType(apiv2.Upgradeable))
}
