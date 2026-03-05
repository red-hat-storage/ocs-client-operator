package controller

import (
	"context"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	nbv1 "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ObcFinalizer                       = nbv1.ObjectBucketFinalizer
	ObjectBucketClaimStatusPhaseFailed = "Failed"
)

// OBCReconciler reconciles a ObjectBucketClaim object
type OBCReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	log    logr.Logger
	ctx    context.Context
}

// SetupWithManager sets up the controller with the Manager
func (r *OBCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Reconcile on Create, Delete, and Update when the object is being deleted (deletionTimestamp set) or when the spec changes.
	obcPredicate := predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		DeleteFunc: func(event.DeleteEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectNew == nil {
				return false
			}
			obcNew, ok := e.ObjectNew.(*nbv1.ObjectBucketClaim)
			if !ok {
				return false
			}
			if !obcNew.GetDeletionTimestamp().IsZero() {
				return true
			}
			if e.ObjectOld == nil {
				return false
			}
			obcOld, ok := e.ObjectOld.(*nbv1.ObjectBucketClaim)
			if !ok {
				return false
			}
			return !equality.Semantic.DeepEqual(obcOld.Spec, obcNew.Spec)
		},
	}

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
//+kubebuilder:rbac:groups=objectbucket.io,resources=objectbuckets,verbs=get;list;watch;update;delete
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients,verbs=get
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;update

func (r *OBCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.ctx = ctx
	r.log = ctrl.LoggerFrom(r.ctx).WithName("OBC")

	r.log.Info("Starting reconcile iteration for OBC", "req", req)

	obc := &nbv1.ObjectBucketClaim{}
	err := r.Get(r.ctx, req.NamespacedName, obc)
	if err != nil {
		if errors.IsNotFound(err) {
			r.log.Info("OBC resource not found. Ignoring since object must be deleted.")
			return reconcile.Result{}, nil
		}
		r.log.Error(err, "failed to get OBC")
		return reconcile.Result{}, fmt.Errorf("failed to get OBC: %v", err)
	}

	if !obc.GetDeletionTimestamp().IsZero() {
		r.log.Info("OBC deleted", "namespaced/name", client.ObjectKeyFromObject(obc))
		storageClient, err := r.getStorageClientFromStorageClass(obc.Spec.StorageClassName)
		if err != nil {
			r.log.Error(err, "failed to get StorageClient for OBC delete")
			return reconcile.Result{}, fmt.Errorf("failed to get StorageClient for OBC delete: %v", err)
		}
		if err := r.notifyObcDeleted(storageClient, types.NamespacedName{Namespace: obc.Namespace, Name: obc.Name}); err != nil {
			r.log.Error(err, "failed to notify provider of OBC deletion", "namespaced/name", client.ObjectKeyFromObject(obc))
			return reconcile.Result{}, fmt.Errorf("failed to in Notify gRPC call of OBC deleted: %v", err)
		}
		if controllerutil.RemoveFinalizer(obc, ObcFinalizer) {
			r.log.Info("removing finalizer from OBC", "namespaced/name", client.ObjectKeyFromObject(obc))
			if err := r.Update(r.ctx, obc); err != nil {
				r.log.Info("Failed to remove finalizer from OBC", "namespaced/name", client.ObjectKeyFromObject(obc))
				return reconcile.Result{}, fmt.Errorf("failed to remove finalizer from OBC: %v", err)
			}
		}
		return reconcile.Result{}, nil
	}

	r.log.Info("OBC created", "namespace", obc.Namespace, "name", obc.Name)
	if controllerutil.AddFinalizer(obc, ObcFinalizer) {
		r.log.Info("Finalizer not found for OBC. Adding finalizer", "namespaced/name", client.ObjectKeyFromObject(obc))
		if err := r.Update(r.ctx, obc); err != nil {
			r.log.Info("Failed to add finalizer to OBC", "namespaced/name", client.ObjectKeyFromObject(obc))
			return reconcile.Result{}, fmt.Errorf("failed to add finalizer to OBC: %v", err)
		}
	}
	storageClient, err := r.getStorageClientFromStorageClass(obc.Spec.StorageClassName)
	if err != nil {
		r.log.Error(err, "failed to get StorageClient for OBC create")
		obc.Status.Phase = ObjectBucketClaimStatusPhaseFailed
		if statusErr := r.Client.Status().Update(r.ctx, obc); statusErr != nil {
			r.log.Error(statusErr, "Failed to update OBC status")
		}
		return reconcile.Result{}, fmt.Errorf("failed to get StorageClient for OBC created: %v", err)
	}
	if err := r.notifyObcCreated(storageClient, obc); err != nil {
		r.log.Error(err, "failed to notify provider of OBC creation", "namespaced/name", client.ObjectKeyFromObject(obc))
		obc.Status.Phase = ObjectBucketClaimStatusPhaseFailed
		if statusErr := r.Client.Status().Update(r.ctx, obc); statusErr != nil {
			r.log.Error(statusErr, "Failed to update OBC status")
		}
		return reconcile.Result{}, fmt.Errorf("failed to in Notify gRPC call of OBC creation: %v", err)

	}
	// Clear Failed status when a retry succeeds
	if obc.Status.Phase == ObjectBucketClaimStatusPhaseFailed {
		obc.Status.Phase = ""
		if statusErr := r.Client.Status().Update(r.ctx, obc); statusErr != nil {
			r.log.Error(statusErr, "Failed to update OBC status after success")
		}
	}
	return reconcile.Result{}, nil
}

// getStorageClientFromStorageClass returns the StorageClient that owns the given StorageClass (via ownerReference).
func (r *OBCReconciler) getStorageClientFromStorageClass(storageClassName string) (*v1alpha1.StorageClient, error) {
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
	if storageClient.Status.ConsumerID == "" || storageClient.Spec.StorageProviderEndpoint == "" {
		return nil, fmt.Errorf("StorageClient %q has no ConsumerID or StorageProviderEndpoint", storageClient.Name)
	}
	return storageClient, nil
}

// notifyObcCreated notifies the provider of the creation of an OBC.
func (r *OBCReconciler) notifyObcCreated(storageClient *v1alpha1.StorageClient, obc *nbv1.ObjectBucketClaim) error {
	pc, err := utils.NewProviderClientForStorageClient(r.ctx, storageClient.Spec.StorageProviderEndpoint)
	if err != nil {
		return err
	}
	defer pc.Close()
	_, err = pc.NotifyObcCreated(r.ctx, storageClient.Status.ConsumerID, obc)
	if err != nil {
		return fmt.Errorf("NotifyObcCreated: %w", err)
	}
	r.log.Info("Notify of OBC created completed", "namespace", obc.Namespace, "name", obc.Name)
	return nil
}

// notifyObcDeleted notifies the provider of the deletion of an OBC.
func (r *OBCReconciler) notifyObcDeleted(storageClient *v1alpha1.StorageClient, obcDetails types.NamespacedName) error {
	pc, err := utils.NewProviderClientForStorageClient(r.ctx, storageClient.Spec.StorageProviderEndpoint)
	if err != nil {
		return err
	}
	defer pc.Close()
	_, err = pc.NotifyObcDeleted(r.ctx, storageClient.Status.ConsumerID, obcDetails)
	if err != nil {
		return fmt.Errorf("NotifyObcDeleted: %w", err)
	}
	r.log.Info("Notify of OBC deleted completed", "namespace", obcDetails.Namespace, "name", obcDetails.Name)
	return nil
}
