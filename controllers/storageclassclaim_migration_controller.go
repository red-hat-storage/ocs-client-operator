package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// for migration of storageclassclaims to storageclaims
	storageClassClaimFinalizer = "storageclassclaim.ocs.openshift.io"
)

// StorageClassClaimReconcile migrates StorageClassClaim objects to StorageClaim
type StorageClassClaimMigrationReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	log logr.Logger
	ctx context.Context
}

func (r *StorageClassClaimMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	onlyCreateEvent := predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(event.DeleteEvent) bool {
			return false
		},
		UpdateFunc: func(event.UpdateEvent) bool {
			return false
		},
		GenericFunc: func(event.GenericEvent) bool {
			return false
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StorageClassClaim{}, builder.WithPredicates(onlyCreateEvent)).
		Complete(r)
}

//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclassclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclassclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclassclaims/finalizers,verbs=update

func (r *StorageClassClaimMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	r.log = log.FromContext(ctx)
	r.ctx = ctx
	r.log.Info("Starting reconcile.")

	storageClassClaim := &v1alpha1.StorageClassClaim{}
	storageClassClaim.Name = req.Name

	if err := r.get(storageClassClaim); err != nil && !kerrors.IsNotFound(err) {
		r.log.Error(err, fmt.Sprintf("failed to get storageclassclaim %q", storageClassClaim.Name))
		return ctrl.Result{}, err
	}

	storageClaim := &v1alpha1.StorageClaim{}
	storageClaim.Name = storageClassClaim.Name

	switch storageClassClaim.Spec.Type {
	case "blockpool":
		storageClaim.Spec.Type = "block"
	case "sharedfilesystem":
		storageClaim.Spec.Type = "sharedfile"
	}

	storageClaim.Spec.EncryptionMethod = storageClassClaim.Spec.EncryptionMethod
	storageClaim.Spec.StorageProfile = storageClassClaim.Spec.StorageProfile
	storageClaim.Spec.StorageClient = storageClassClaim.Spec.StorageClient.DeepCopy()

	r.log.Info(fmt.Sprintf("Migrating storageclassclaim %q", storageClassClaim.Name))
	if err := r.create(storageClaim); err != nil && !kerrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("failed to create storageclaims %q: %v", storageClaim.Name, err)
	}

	for idx := range storageClassClaim.Status.SecretNames {
		secret := &corev1.Secret{}
		secret.Name = storageClassClaim.Status.SecretNames[idx]
		secret.Namespace = storageClassClaim.Spec.StorageClient.Namespace
		if err := r.delete(secret); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to delete secret %s: %v", client.ObjectKeyFromObject(secret), err)
		}
	}

	// remove finalizer on existing storageclassclaim
	finalizerUpdated := controllerutil.RemoveFinalizer(storageClassClaim, storageClassClaimFinalizer)
	if finalizerUpdated {
		if err := r.update(storageClassClaim); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove finalizer on storageclassclaim %q: %v", storageClassClaim.Name, err)
		}
	}

	// migration is successful delete the storageclassclaim
	if err := r.delete(storageClassClaim); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to delete storageclassclaim %q: %v", storageClassClaim.Name, err)
	}

	r.log.Info(fmt.Sprintf("Successfully migrated storageclassclaim %q to storageclass %q", storageClassClaim.Name, storageClaim.Name))
	return ctrl.Result{}, nil
}

func (r *StorageClassClaimMigrationReconciler) get(obj client.Object, opts ...client.GetOption) error {
	return r.Get(r.ctx, client.ObjectKeyFromObject(obj), obj, opts...)
}

func (r *StorageClassClaimMigrationReconciler) create(obj client.Object, opts ...client.CreateOption) error {
	return r.Create(r.ctx, obj, opts...)
}

func (r *StorageClassClaimMigrationReconciler) update(obj client.Object, opts ...client.UpdateOption) error {
	return r.Update(r.ctx, obj, opts...)
}

func (r *StorageClassClaimMigrationReconciler) delete(obj client.Object, opts ...client.DeleteOption) error {
	if err := r.Delete(r.ctx, obj, opts...); err != nil && !kerrors.IsNotFound(err) {
		return err
	}
	return nil
}
