package controller

import (
	"context"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	nbv1 "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	"go.uber.org/multierr"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
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
	// Reconcile on Create, Delete, and Update when the object is being deleted (deletionTimestamp set).
	obcPredicate := predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		DeleteFunc: func(event.DeleteEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectNew == nil {
				return false
			}
			obc, ok := e.ObjectNew.(*nbv1.ObjectBucketClaim)
			if !ok {
				return false
			}
			return !obc.GetDeletionTimestamp().IsZero()
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
		r.log.Info("releasing OBC resources", "namespaced/name", client.ObjectKeyFromObject(obc))
		ob, cm, secret, err := r.getResources(obc)
		if err != nil {
			return reconcile.Result{}, err
		}
		if err := r.deleteResources(ob, cm, secret); err != nil {
			r.log.Error(err, "failed to delete resources for OBC delete")
			return reconcile.Result{}, fmt.Errorf("failed to delete resources for OBC delete: %v", err)
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
	pc, err := NewProviderClientForStorageClient(r.ctx, storageClient)
	if err != nil {
		return fmt.Errorf("create provider client: %w", err)
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
	pc, err := NewProviderClientForStorageClient(r.ctx, storageClient)
	if err != nil {
		return fmt.Errorf("create provider client: %w", err)
	}
	defer pc.Close()
	_, err = pc.NotifyObcDeleted(r.ctx, storageClient.Status.ConsumerID, obcDetails)
	if err != nil {
		return fmt.Errorf("NotifyObcDeleted: %w", err)
	}
	r.log.Info("Notify of OBC deleted completed", "namespace", obcDetails.Namespace, "name", obcDetails.Name)
	return nil
}

// NewProviderClientForStorageClient creates an OCS provider gRPC client for the given StorageClient.
func NewProviderClientForStorageClient(ctx context.Context, sc *v1alpha1.StorageClient) (*providerClient.OCSProviderClient, error) {
	pc, err := providerClient.NewProviderClient(ctx, sc.Spec.StorageProviderEndpoint, utils.OcsClientTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider client with endpoint %v: %w", sc.Spec.StorageProviderEndpoint, err)
	}
	return pc, nil
}

// getResources gets the resources that were created as part of the OBC provisioning.
// The names of the ConfigMap and Secret are always the same as the OBC name.
// The names of the OB's are of the following format: "obc-<namespace_of_OBC>-<OBC_name>"
func (r *OBCReconciler) getResources(obc *nbv1.ObjectBucketClaim) (ob *nbv1.ObjectBucket, cm *corev1.ConfigMap, secret *corev1.Secret, combinedErr error) {
	obName := fmt.Sprintf("obc-%s-%s", obc.Namespace, obc.Name)
	ob = &nbv1.ObjectBucket{}
	if err := r.Get(r.ctx, types.NamespacedName{Name: obName}, ob); err != nil {
		ob = nil
		if !errors.IsNotFound(err) {
			r.log.Error(err, "failed to get OB", "name", obName)
			multierr.AppendInto(&combinedErr, err)
		}
	}
	cm = &corev1.ConfigMap{}
	if err := r.Get(r.ctx, types.NamespacedName{Namespace: obc.Namespace, Name: obc.Name}, cm); err != nil {
		cm = nil
		if !errors.IsNotFound(err) {
			r.log.Error(err, "failed to get config map", "namespace", obc.Namespace, "name", obc.Name)
			multierr.AppendInto(&combinedErr, err)
		}
	}
	secret = &corev1.Secret{}
	if err := r.Get(r.ctx, types.NamespacedName{Namespace: obc.Namespace, Name: obc.Name}, secret); err != nil {
		secret = nil
		if !errors.IsNotFound(err) {
			r.log.Error(err, "failed to get secret", "namespace", obc.Namespace, "name", obc.Name)
			multierr.AppendInto(&combinedErr, err)
		}
	}
	return ob, cm, secret, combinedErr
}

// deleteResources handles the related resources that were created as part of the OBC provisioning
// Since the secret and configmap's ownerReference is the OBC they will be garbage collected once their finalizers are removed.
// The OB must be explicitly deleted since it is a global resource and cannot have a namespaced ownerReference.
func (r *OBCReconciler) deleteResources(ob *nbv1.ObjectBucket, cm *corev1.ConfigMap, secret *corev1.Secret) (combinedErr error) {
	if delErr := r.releaseAndDeleteOB(ob); delErr != nil {
		r.log.Error(delErr, "error deleting OB", "name", ob.Name)
		multierr.AppendInto(&combinedErr, fmt.Errorf("failed to delete OB: %w", delErr))
	}
	if delErr := r.releaseObcSecret(secret); delErr != nil {
		r.log.Error(delErr, "error releasing secret", "name", secret.Name, "namespace", secret.Namespace)
		multierr.AppendInto(&combinedErr, fmt.Errorf("failed to release secret: %w", delErr))
	}
	if delErr := r.releaseObcConfigMap(cm); delErr != nil {
		r.log.Error(delErr, "error releasing configMap", "name", cm.Name, "namespace", cm.Namespace)
		multierr.AppendInto(&combinedErr, fmt.Errorf("failed to release configMap: %w", delErr))
	}
	return combinedErr
}

// The OB does not have an ownerReference and must be explicitly deleted after its finalizer is removed.
func (r *OBCReconciler) releaseAndDeleteOB(ob *nbv1.ObjectBucket) error {
	if ob == nil {
		r.log.Info("got nil OB, skipping")
		return nil
	}

	if controllerutil.RemoveFinalizer(ob, nbv1.ObjectBucketFinalizer) {
		r.log.Info("removing finalizer from OB", "name", ob.Name)
		if err := r.Update(r.ctx, ob); err != nil {
			r.log.Info("Failed to remove finalizer from OB", "name", ob.Name)
			return fmt.Errorf("failed to remove finalizer from OB: %v", err)
		}
	}

	r.log.Info("deleting OB", "name", ob.Name)
	if err := r.Delete(r.ctx, ob); err != nil {
		r.log.Info("Failed to delete OB", "name", ob.Name)
		return fmt.Errorf("failed to delete OB: %v", err)
	}

	r.log.Info("OB deleted", "name", ob.Name)
	return nil
}

// releaseSecret releases the finalizer from the secret.
// The secret will be garbage collected since its ownerReference refers to the parent OBC.
func (r *OBCReconciler) releaseObcSecret(secret *corev1.Secret) (err error) {
	if secret == nil {
		r.log.Info("got nil secret, skipping")
		return nil
	}

	if controllerutil.RemoveFinalizer(secret, nbv1.ObjectBucketFinalizer) {
		r.log.Info("removing finalizer from secret", "name", secret.Name)
		if err := r.Update(r.ctx, secret); err != nil {
			r.log.Info("Failed to remove finalizer from secret", "namespaced/name", client.ObjectKeyFromObject(secret))
			return fmt.Errorf("failed to remove finalizer from secret: %v", err)
		}
	}

	r.log.Info("secret finalizer removed", "namespaced/name", client.ObjectKeyFromObject(secret))
	return nil
}

// releaseObcConfigMap releases the finalizer from the configmap.
// The configmap will be garbage collected since its ownerReference refers to the parent OBC.
func (r *OBCReconciler) releaseObcConfigMap(cm *corev1.ConfigMap) (err error) {
	if cm == nil {
		r.log.Info("got nil configmap, skipping")
		return nil
	}

	if controllerutil.RemoveFinalizer(cm, nbv1.ObjectBucketFinalizer) {
		r.log.Info("removing finalizer from configmap", "name", cm.Name)
		if err := r.Update(r.ctx, cm); err != nil {
			r.log.Info("Failed to remove finalizer from configmap", "namespaced/name", client.ObjectKeyFromObject(cm))
			return fmt.Errorf("failed to remove finalizer from configmap: %v", err)
		}
	}

	r.log.Info("configmap finalizer removed", "namespaced/name", client.ObjectKeyFromObject(cm))
	return nil
}
