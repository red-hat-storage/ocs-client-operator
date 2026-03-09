package controller

import (
	"context"
	"fmt"
	"slices"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"

	"github.com/go-logr/logr"
	nbv1 "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	storagev1 "k8s.io/api/storage/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ObcFinalizer                       = nbv1.ObjectBucketFinalizer
	ObjectBucketClaimStatusPhaseFailed = "Failed"
)

// ObcReconciler reconciles a ObjectBucketClaim object
type ObcReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

type obcReconcile struct {
	*ObcReconciler
	ctx context.Context
	log logr.Logger
	obc nbv1.ObjectBucketClaim
}

// SetupWithManager sets up the controller with the Manager
func (r *ObcReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Reconcile on Create, Delete, and Update when the object is being deleted or when the spec (generation) changes.
	obcPredicate := predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return !obj.GetDeletionTimestamp().IsZero()
		}),
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named("ObjectBucketClaim").
		For(
			&nbv1.ObjectBucketClaim{},
			builder.WithPredicates(obcPredicate),
		).
		Complete(r)
}

//+kubebuilder:rbac:groups=objectbucket.io,resources=objectbucketclaims,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=objectbucket.io,resources=objectbucketclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients,verbs=get
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get

func (r *ObcReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	handler := obcReconcile{ObcReconciler: r}
	return handler.reconcile(ctx, req)
}

// reconcile is the main reconciliation loop for the OBC.
func (r *obcReconcile) reconcile(ctx context.Context, req ctrl.Request) (reconcile.Result, error) {
	r.log = ctrl.LoggerFrom(ctx).WithName("OBC")
	r.ctx = ctx
	r.obc.Name = req.Name
	r.obc.Namespace = req.Namespace

	r.log.Info("Starting reconcile iteration for OBC", "req", req)
	if err := r.Get(r.ctx, req.NamespacedName, &r.obc); err != nil {
		if kerrors.IsNotFound(err) {
			r.log.Info("OBC resource not found. Ignoring since object must be deleted.")
			return reconcile.Result{}, nil
		}
		r.log.Error(err, "failed to get OBC")
		return reconcile.Result{}, fmt.Errorf("failed to get OBC: %v", err)
	}

	initialPhase := r.obc.Status.Phase
	result, reconcileErr := r.reconcilePhases()

	var statusErr error
	if r.obc.Status.Phase != initialPhase {
		statusErr = r.Client.Status().Update(r.ctx, &r.obc)
		if statusErr != nil {
			r.log.Error(statusErr, "Failed to update OBC status.")
		}
	}
	if reconcileErr != nil {
		return reconcile.Result{}, reconcileErr
	}
	if statusErr != nil {
		return reconcile.Result{}, statusErr
	}

	return result, nil
}

// reconcilePhases handles the different phases of the OBC reconciliation.
func (r *obcReconcile) reconcilePhases() (ctrl.Result, error) {
	storageClient, err := r.getStorageClientFromStorageClass(r.obc.Spec.StorageClassName)
	if err != nil {
		r.setFailedPhaseIfNotDeleting()
		r.log.Error(err, "failed to get StorageClient")
		return reconcile.Result{}, fmt.Errorf("failed to get StorageClient: %w", err)
	}

	ocsProviderClient, err := utils.NewProviderClientForStorageClient(
		r.ctx,
		storageClient.Spec.StorageProviderEndpoint,
	)
	if err != nil {
		r.setFailedPhaseIfNotDeleting()
		r.log.Error(err, "failed to create provider client")
		return reconcile.Result{}, err
	}
	defer ocsProviderClient.Close()

	if !r.obc.GetDeletionTimestamp().IsZero() {
		return r.deletionPhase(ocsProviderClient, storageClient)
	}

	return r.createOrUpdatePhase(ocsProviderClient, storageClient)
}

// createOrUpdatePhase handles the create/update phase of the OBC reconciliation.
func (r *obcReconcile) createOrUpdatePhase(
	ocsProviderClient *providerClient.OCSProviderClient,
	storageClient *v1alpha1.StorageClient,
) (ctrl.Result, error) {
	r.log.Info("OBC created/updated", "namespaced/name", client.ObjectKeyFromObject(&r.obc))
	if controllerutil.AddFinalizer(&r.obc, ObcFinalizer) {
		r.log.Info("Finalizer not found for OBC. Adding finalizer", "namespaced/name", client.ObjectKeyFromObject(&r.obc))
		if err := r.Update(r.ctx, &r.obc); err != nil {
			r.log.Info("Failed to add finalizer to OBC", "namespaced/name", client.ObjectKeyFromObject(&r.obc))
			return reconcile.Result{}, fmt.Errorf("failed to add finalizer to OBC: %v", err)
		}
		return reconcile.Result{Requeue: true}, nil
	}

	if _, err := ocsProviderClient.NotifyObcCreated(r.ctx, storageClient.Status.ConsumerID, &r.obc); err != nil {
		r.log.Error(err, "failed to notify provider of OBC created/updated", "namespaced/name", client.ObjectKeyFromObject(&r.obc))
		r.setFailedPhaseIfNotDeleting()
		return reconcile.Result{}, fmt.Errorf("failed to call gRPC call Notify - NotifyObcCreated: %w", err)
	}
	r.log.Info("Notify of OBC created/updated completed", "namespaced/name", client.ObjectKeyFromObject(&r.obc))

	// Clear Failed status when a retry succeeds
	if r.obc.Status.Phase == ObjectBucketClaimStatusPhaseFailed {
		r.obc.Status.Phase = ""
	}

	return reconcile.Result{}, nil
}

// deletionPhase handles the deletion phase of the OBC reconciliation.
func (r *obcReconcile) deletionPhase(
	ocsProviderClient *providerClient.OCSProviderClient,
	storageClient *v1alpha1.StorageClient,
) (ctrl.Result, error) {
	r.log.Info("OBC deleted", "namespaced/name", client.ObjectKeyFromObject(&r.obc))

	obcNamespacedName := types.NamespacedName{Namespace: r.obc.Namespace, Name: r.obc.Name}
	if _, err := ocsProviderClient.NotifyObcDeleted(r.ctx, storageClient.Status.ConsumerID, obcNamespacedName); err != nil {
		r.log.Error(err, "failed to notify provider of OBC deletion", "namespaced/name", client.ObjectKeyFromObject(&r.obc))
		return reconcile.Result{}, fmt.Errorf("failed to call gRPC call Notify - NotifyObcDeleted: %w", err)
	}
	r.log.Info("Notify of OBC deleted completed", "namespaced/name", client.ObjectKeyFromObject(&r.obc))
	if controllerutil.RemoveFinalizer(&r.obc, ObcFinalizer) {
		r.log.Info("removing finalizer from OBC", "namespaced/name", client.ObjectKeyFromObject(&r.obc))
		if err := r.Update(r.ctx, &r.obc); err != nil {
			r.log.Info("Failed to remove finalizer from OBC", "namespaced/name", client.ObjectKeyFromObject(&r.obc))
			return reconcile.Result{}, fmt.Errorf("failed to remove finalizer from OBC: %v", err)
		}
	}
	return reconcile.Result{}, nil
}

// getStorageClientFromStorageClass returns the StorageClient that owns the given StorageClass (via ownerReference).
func (r *obcReconcile) getStorageClientFromStorageClass(storageClassName string) (*v1alpha1.StorageClient, error) {
	sc := &storagev1.StorageClass{}
	if err := r.Get(r.ctx, types.NamespacedName{Name: storageClassName}, sc); err != nil {
		return nil, fmt.Errorf("get StorageClass %q: %w", storageClassName, err)
	}
	ownerStorageClientIndex := slices.IndexFunc(sc.OwnerReferences, func(owner metav1.OwnerReference) bool {
		return owner.Kind == "StorageClient"
	})
	if ownerStorageClientIndex == -1 {
		return nil, fmt.Errorf("StorageClass %q has no StorageClient ownerReference", storageClassName)
	}
	storageClient := &v1alpha1.StorageClient{}
	storageClientName := sc.OwnerReferences[ownerStorageClientIndex].Name
	if err := r.Get(r.ctx, types.NamespacedName{Name: storageClientName}, storageClient); err != nil {
		return nil, fmt.Errorf("get StorageClient %q (owner of StorageClass %q): %w", storageClientName, storageClassName, err)
	}
	return storageClient, nil
}

// setFailedPhaseIfNotDeleting sets the OBC phase to Failed only when the OBC is not being deleted.
func (r *obcReconcile) setFailedPhaseIfNotDeleting() {
	if r.obc.GetDeletionTimestamp().IsZero() {
		r.obc.Status.Phase = ObjectBucketClaimStatusPhaseFailed
	}
}
