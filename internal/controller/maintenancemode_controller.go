package controller

import (
	"context"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	ramenv1alpha1 "github.com/ramendr/ramen/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	providerclient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	MaintenanceModeCRDName = "maintenancemodes.ramendr.openshift.io"
)

// MaintenanceModeReconciler reconciles a ClusterVersion object
type MaintenanceModeReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	log logr.Logger
	ctx context.Context
}

// SetupWithManager sets up the controller with the Manager.
func (r *MaintenanceModeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	generationChangePredicate := predicate.GenerationChangedPredicate{}
	maintenanceModeChangedPredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			oldObj := e.ObjectOld.(*v1alpha1.StorageClient)
			newObj := e.ObjectNew.(*v1alpha1.StorageClient)
			return oldObj.Status.InMaintenanceMode != newObj.Status.InMaintenanceMode
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("MaintenanceMode").
		Watches(
			&ramenv1alpha1.MaintenanceMode{},
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(generationChangePredicate),
		).
		Watches(
			&v1alpha1.StorageClient{},
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(maintenanceModeChangedPredicate),
		).
		Complete(r)
}

//+kubebuilder:rbac:groups=ramendr.openshift.io,resources=maintenancemodes,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=ramendr.openshift.io,resources=maintenancemodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients,verbs=get;list;watch

func (r *MaintenanceModeReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	r.ctx = ctx
	r.log = log.FromContext(ctx)
	r.log.Info("Starting reconcile")

	//TODO: This controller assumes that there is only one client and that the maintenance mode is created only for that
	// client. The correct way to fix this would be to use the mmode's targetID (which is the replicationID) and to
	// fetch the corresponding VRC as the VRC is labeled with targetID and then fetch the StorageClient and put it to
	// maintenanceMode

	maintenanceModes := &ramenv1alpha1.MaintenanceModeList{}
	if err := r.list(maintenanceModes); err != nil {
		r.log.Error(err, "failed to list the MaintenanceMode CRs")
		return reconcile.Result{}, err
	}

	storageClients := &v1alpha1.StorageClientList{}
	if err := r.list(storageClients); err != nil {
		r.log.Error(err, "failed to list the Storage Clients")
		return reconcile.Result{}, err
	}

	if len(storageClients.Items) != 1 {
		r.log.Error(
			fmt.Errorf("expected 1 StorageClient found %d", len(storageClients.Items)),
			"invalid number of StorageClients found",
		)
		return reconcile.Result{}, fmt.Errorf("expected 1 StorageClient found %d", len(storageClients.Items))
	}

	needsMaintenanceMode := false

	for i := range maintenanceModes.Items {
		mm := &maintenanceModes.Items[i]
		storageClient := &storageClients.Items[0]
		needsMaintenanceMode = true
		if storageClient.Status.InMaintenanceMode {
			if err := r.updateStatusCompletedForMM(mm); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update status for MaintenanceMode %s: %w", mm.Name, err)
			}
		}
	}

	for i := range storageClients.Items {
		storageClient := &storageClients.Items[i]
		if needsMaintenanceMode != storageClient.Status.InMaintenanceMode {
			if err := r.toggleMaintenanceModeForClient(storageClient, needsMaintenanceMode); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

func (r *MaintenanceModeReconciler) toggleMaintenanceModeForClient(storageClient *v1alpha1.StorageClient, enable bool) error {
	providerClient, err := providerclient.NewProviderClient(
		r.ctx,
		storageClient.Spec.StorageProviderEndpoint,
		utils.OcsClientTimeout,
	)
	if err != nil {
		return fmt.Errorf(
			"failed to create provider client with endpoint %v: %v",
			storageClient.Spec.StorageProviderEndpoint,
			err,
		)
	}
	// Close client-side connections.
	defer providerClient.Close()

	_, err = providerClient.RequestMaintenanceMode(r.ctx, storageClient.Status.ConsumerID, enable)
	if err != nil {
		return fmt.Errorf("failed to Request maintenance mode: %v", err)
	}
	return nil
}

func (r *MaintenanceModeReconciler) updateStatusCompletedForMM(maintenanceMode *ramenv1alpha1.MaintenanceMode) error {
	// Ramen reads the State and Conditions in order to determine that the MaintenanceMode is Completed

	condition := metav1.Condition{
		Type:               string(ramenv1alpha1.MModeConditionFailoverActivated),
		Status:             metav1.ConditionTrue,
		Reason:             string(ramenv1alpha1.MModeStateCompleted),
		ObservedGeneration: maintenanceMode.Generation,
	}

	updateRequired := false
	updateRequired = updateRequired || (maintenanceMode.Status.State != ramenv1alpha1.MModeStateCompleted)
	updateRequired = updateRequired || slices.Contains(maintenanceMode.Status.Conditions, condition)

	if updateRequired {
		maintenanceMode.Status.State = ramenv1alpha1.MModeStateCompleted
		maintenanceMode.Status.ObservedGeneration = maintenanceMode.Generation
		meta.SetStatusCondition(&maintenanceMode.Status.Conditions, condition)

		if err := r.Client.Status().Update(r.ctx, maintenanceMode); err != nil {
			return err
		}
	}
	return nil
}

func (r *MaintenanceModeReconciler) list(obj client.ObjectList, opts ...client.ListOption) error {
	return r.List(r.ctx, obj, opts...)
}
