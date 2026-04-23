package controller

import (
	"context"
	"errors"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	obcControllerFinalizer = "ocs.openshift.io/obccleanup"
)

// errStorageClassNoStorageClientOwner is returned when the OBC's StorageClass is not
// owned by a StorageClient. The reconciler treats this as a no-op (success, no requeue).
var errStorageClassNoStorageClientOwner = errors.New("StorageClass has no StorageClient ownerReference")

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
	return ctrl.NewControllerManagedBy(mgr).
		Named("ObjectBucketClaim").
		For(
			&nbv1.ObjectBucketClaim{},
			// we filter out updates on status intentionally (it is updated from outside)
			builder.WithPredicates(
				predicate.Or(
					predicate.GenerationChangedPredicate{},
					predicate.LabelChangedPredicate{},
				),
			),
		).
		Complete(r)
}

//+kubebuilder:rbac:groups=objectbucket.io,resources=objectbucketclaims,verbs=get;list;watch;update
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

	r.log.Info("Starting reconcile iteration for OBC")
	if err := r.Get(r.ctx, req.NamespacedName, &r.obc); err != nil {
		if kerrors.IsNotFound(err) {
			r.log.Info("OBC resource not found. Ignoring since object must be deleted")
			return reconcile.Result{}, nil
		}
		r.log.Error(err, "failed to get OBC")
		return reconcile.Result{}, fmt.Errorf("failed to get OBC: %v", err)
	}

	result, reconcileErr := r.reconcilePhases()

	if reconcileErr != nil {
		return reconcile.Result{}, reconcileErr
	}

	return result, nil
}

// reconcilePhases handles the different phases of the OBC reconciliation.
func (r *obcReconcile) reconcilePhases() (ctrl.Result, error) {
	storageClient, err := r.getStorageClientFromStorageClass(r.obc.Spec.StorageClassName)
	if err != nil {
		if errors.Is(err, errStorageClassNoStorageClientOwner) {
			r.log.Info("StorageClass is not owned by a StorageClient; finish OBC reconciliation",
				"storageClassName", r.obc.Spec.StorageClassName)
			return reconcile.Result{}, nil
		}
		r.log.Error(err, "failed to get StorageClient")
		return reconcile.Result{}, fmt.Errorf("failed to get StorageClient: %w", err)
	}

	ocsProviderClient, err := providerClient.NewProviderClient(r.ctx, storageClient.Spec.StorageProviderEndpoint, utils.OcsClientTimeout)
	if err != nil {
		r.log.Error(err, "failed to create provider client")
		return reconcile.Result{}, err
	}
	defer ocsProviderClient.Close()

	var result ctrl.Result
	if r.obc.GetDeletionTimestamp().IsZero() {
		result, err = r.handleObcCreationOrUpdate(ocsProviderClient, storageClient)
	} else {
		result, err = r.handleObcDeletion(ocsProviderClient, storageClient)
	}
	return result, err
}

// handleObcCreationOrUpdate handles the creation or update phase of the OBC reconciliation.
func (r *obcReconcile) handleObcCreationOrUpdate(
	ocsProviderClient *providerClient.OCSProviderClient,
	storageClient *v1alpha1.StorageClient,
) (ctrl.Result, error) {
	r.log.Info("OBC created/updated")

	shouldUpdateMetaData := false
	if controllerutil.AddFinalizer(&r.obc, obcControllerFinalizer) {
		r.log.Info("Finalizer not found for OBC. Adding finalizer")
		shouldUpdateMetaData = true
	}

	// this label is used to identify the StorageClient that the OBC is associated with
	if utils.AddLabel(&r.obc, storageClientNameLabel, storageClient.Name) {
		r.log.Info("Label for StorageClient name not found for OBC. Adding label")
		shouldUpdateMetaData = true
	}

	if shouldUpdateMetaData {
		if err := r.Update(r.ctx, &r.obc); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update OBC metadata: %v", err)
		}
	}

	if _, err := ocsProviderClient.NotifyObcCreated(r.ctx, storageClient.Status.ConsumerID, &r.obc); err != nil {
		r.log.Error(err, "failed to notify provider of OBC created/updated")
		return reconcile.Result{}, fmt.Errorf("failed to call gRPC call Notify - NotifyObcCreated: %w", err)
	}
	r.log.Info("Notify of OBC created/updated completed")
	return reconcile.Result{}, nil
}

// handleObcDeletion handles the deletion phase of the OBC reconciliation.
func (r *obcReconcile) handleObcDeletion(
	ocsProviderClient *providerClient.OCSProviderClient,
	storageClient *v1alpha1.StorageClient,
) (ctrl.Result, error) {
	r.log.Info("OBC deleted")

	obcNamespacedName := client.ObjectKeyFromObject(&r.obc)
	if _, err := ocsProviderClient.NotifyObcDeleted(r.ctx, storageClient.Status.ConsumerID, obcNamespacedName); err != nil {
		r.log.Error(err, "failed to notify provider of OBC deletion")
		return reconcile.Result{}, fmt.Errorf("failed to call gRPC call Notify - NotifyObcDeleted: %w", err)
	}
	r.log.Info("Notify of OBC deleted completed")
	if controllerutil.RemoveFinalizer(&r.obc, obcControllerFinalizer) {
		r.log.Info("removing finalizer from OBC")
		if err := r.Update(r.ctx, &r.obc); err != nil {
			r.log.Info("Failed to remove finalizer from OBC")
			return reconcile.Result{}, fmt.Errorf("failed to remove finalizer from OBC: %v", err)
		}
	}
	return reconcile.Result{}, nil
}

// getStorageClientFromStorageClass returns the StorageClient that owns the given StorageClass (via ownerReference).
func (r *obcReconcile) getStorageClientFromStorageClass(storageClassName string) (*v1alpha1.StorageClient, error) {
	storageClass := &storagev1.StorageClass{}
	storageClass.Name = storageClassName
	if err := r.Get(r.ctx, client.ObjectKeyFromObject(storageClass), storageClass); err != nil {
		return nil, fmt.Errorf("get StorageClass %q: %w", storageClassName, err)
	}
	ownerStorageClientIndex := slices.IndexFunc(
		storageClass.OwnerReferences,
		func(owner metav1.OwnerReference) bool {
			return owner.Kind == "StorageClient"
		},
	)
	if ownerStorageClientIndex == -1 {
		return nil, fmt.Errorf("%w: %q", errStorageClassNoStorageClientOwner, storageClassName)
	}
	storageClient := &v1alpha1.StorageClient{}
	storageClient.Name = storageClass.OwnerReferences[ownerStorageClientIndex].Name
	if err := r.Get(r.ctx, client.ObjectKeyFromObject(storageClient), storageClient); err != nil {
		return nil, fmt.Errorf("get StorageClient %q (owner of StorageClass %q): %w", storageClient.Name, storageClass.Name, err)
	}
	return storageClient, nil
}
