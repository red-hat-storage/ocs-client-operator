package controller

import (
	"context"
	"slices"

	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	"github.com/go-logr/logr"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// crdsWatchedForPresenceRestart lists CRDs whose install/removal must match process startup
// (manager cache and conditional controller registration; see cmd/main.go).
// IMPORTANT - only add the cases where the dynamic watch for the CRD did not match the case.
var crdsWatchedForPresenceRestart = []string{
	ObjectBucketClaimCrdName,
	MaintenanceModeCRDName,
}

type CrdsPresenceReconciler struct {
	client.Client
	AvailableCrds     map[string]bool
	ShutdownContainer func()
	log               logr.Logger
}

func (r *CrdsPresenceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("CrdsPresence").
		For(
			&extv1.CustomResourceDefinition{},
			builder.WithPredicates(
				predicate.NewPredicateFuncs(func(obj client.Object) bool {
					return slices.Contains(crdsWatchedForPresenceRestart, obj.GetName())
				}),
				utils.EventTypePredicate(true, false, true, false),
			),
		).
		Complete(r)
}

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

func (r *CrdsPresenceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log = ctrl.LoggerFrom(ctx).WithName("CrdsPresence")

	for _, name := range crdsWatchedForPresenceRestart {
		crd := &metav1.PartialObjectMetadata{}
		crd.SetGroupVersionKind(extv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
		crd.Name = name
		if err := r.Get(ctx, client.ObjectKeyFromObject(crd), crd); client.IgnoreNotFound(err) != nil {
			r.log.Error(err, "Failed to get CRD", "CRD", crd.Name)
			return ctrl.Result{}, err
		}
		presentNow := crd.UID != ""
		if r.AvailableCrds[name] != presentNow {
			r.log.Info("CRD presence changed, will restart container",
				"presence at manager start", r.AvailableCrds[name],
				"presence now", presentNow,
			)
			r.ShutdownContainer()
			return ctrl.Result{}, nil
		}
	}
	return ctrl.Result{}, nil
}
