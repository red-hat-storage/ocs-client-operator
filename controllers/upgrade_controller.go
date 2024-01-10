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

package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	"github.com/blang/semver/v4"
	"github.com/go-logr/logr"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/operator-framework/operator-lib/conditions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	ocsClientOperatorSubscriptionPackageName = "ocs-client-operator"
	// TODO(lgangava): there can be "eus" or "fast" or "candidate" supported channels
	// 	and custom channels also might exist, either get this data as part of a configmap
	// 	or cut the name by '-'
	subscriptionChannelNamePrefix = "stable-"
	controllerName                = "upgrade"
	upgradeConditionReason        = "ConstraintsSatisfied"
	dummyPatchVersion             = ".99"
)

type UpgradeReconciler struct {
	client.Client
	ctx context.Context
	log logr.Logger

	Scheme            *runtime.Scheme
	OperatorCondition conditions.Condition
	OperatorNamespace string
}

func (r *UpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	storageClientDesiredVersionPredicate := predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}

			oldObj, _ := e.ObjectOld.(*v1alpha1.StorageClient)
			newObj, _ := e.ObjectNew.(*v1alpha1.StorageClient)
			if oldObj == nil || newObj == nil {
				return false
			}

			return oldObj.Status.DesiredOperatorVersion != newObj.Status.DesiredOperatorVersion
		},
	}
	ocsClientOperatorSubscriptionPredicate := predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectNew == nil {
				return false
			}

			newObj, _ := e.ObjectNew.(*opv1a1.Subscription)
			if newObj == nil {
				return false
			}

			return newObj.Spec.Package == ocsClientOperatorSubscriptionPackageName
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return false
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		Watches(&v1alpha1.StorageClient{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(storageClientDesiredVersionPredicate)).
		Watches(&opv1a1.Subscription{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(ocsClientOperatorSubscriptionPredicate)).
		Complete(r)
}

//+kubebuilder:rbac:groups=operators.coreos.com,resources=subscriptions,verbs=get;list;watch
//+kubebuilder:rbac:groups=operators.coreos.com,resources=operatorconditions,verbs=get;list;update;watch

func (r *UpgradeReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	r.ctx = ctx
	r.log = log.FromContext(ctx)

	r.log.Info("starting reconcile")
	result, err := r.reconcilePhases()
	if err != nil {
		r.log.Error(err, "an error occurred during reconcilePhases")
	}
	r.log.Info("reconciling completed")

	return result, err
}

func (r *UpgradeReconciler) reconcilePhases() (ctrl.Result, error) {
	if err := r.reconcileOperatorCondition(); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *UpgradeReconciler) reconcileOperatorCondition() error {

	var messages []string
	currentVersion, err := r.getCurrentOperatorVersion()
	if err != nil {
		r.log.Error(err, "failed to get current operator version")
		messages = append(messages, "failed to get current operator version")
	}
	desiredVersion, err := r.getDesiredOperatorVersion()
	if err != nil {
		r.log.Error(err, "failed to get desired operator version")
		messages = append(messages, "failed to get desired operator version")
	}

	if len(messages) > 0 {
		return r.setOperatorCondition(metav1.ConditionFalse, strings.Join(messages, "; "))
	}

	if currentVersion == nil || desiredVersion == nil {
		return r.setOperatorCondition(metav1.ConditionTrue, "No errors are reported.")
	}

	if currentVersion.Major > desiredVersion.Major || currentVersion.Minor > desiredVersion.Minor {
		return r.setOperatorCondition(metav1.ConditionFalse, "Current operator version is ahead of desired operator version")
	}

	return r.setOperatorCondition(metav1.ConditionTrue, "No errors are reported.")
}

func (r *UpgradeReconciler) setOperatorCondition(isUpgradeable metav1.ConditionStatus, message string) error {
	return r.OperatorCondition.
		Set(r.ctx, isUpgradeable, conditions.WithReason(upgradeConditionReason), conditions.WithMessage(message))
}

// returns current version of the operator based on the subscription
func (r *UpgradeReconciler) getCurrentOperatorVersion() (*semver.Version, error) {
	subscriptionList := &opv1a1.SubscriptionList{}
	if err := r.list(subscriptionList, client.InNamespace(r.OperatorNamespace)); err != nil {
		return nil, err
	}

	subscription := utils.Find(subscriptionList.Items, func(sub *opv1a1.Subscription) bool {
		return sub.Spec.Package == ocsClientOperatorSubscriptionPackageName
	})

	if subscription == nil {
		r.log.Info("subscription of ocs-client-operator doesn't exist")
		return nil, nil
	}

	channel := subscription.Spec.Channel
	version, ok := strings.CutPrefix(channel, subscriptionChannelNamePrefix)
	if !ok {
		return nil, fmt.Errorf("subscription doesn't refer to a stable channel")
	}
	// appending patchversion to pass semver checks
	version += dummyPatchVersion

	currentVersion, err := semver.Make(version)
	if err != nil {
		return nil, err
	}

	return &currentVersion, nil
}

// returns desired version of the operator based on storageclients
func (r *UpgradeReconciler) getDesiredOperatorVersion() (*semver.Version, error) {
	storageClientList := &v1alpha1.StorageClientList{}
	if err := r.list(storageClientList); err != nil {
		return nil, err
	}

	if len(storageClientList.Items) == 0 {
		r.log.Info("no storageclients exist")
		return nil, nil
	}

	// appending patchversion to pass semver checks
	oldest, err := semver.Make(storageClientList.Items[0].Status.DesiredOperatorVersion + dummyPatchVersion)
	if err != nil {
		return nil, err
	}

	for idx := range storageClientList.Items {
		current, err := semver.Make(storageClientList.Items[idx].Status.DesiredOperatorVersion + dummyPatchVersion)
		if err != nil {
			return nil, err
		}
		if current.LE(oldest) {
			oldest = current
		}
	}

	return &oldest, nil
}

func (r *UpgradeReconciler) list(list client.ObjectList, opts ...client.ListOption) error {
	return r.Client.List(r.ctx, list, opts...)
}
