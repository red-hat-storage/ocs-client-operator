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

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	v1alpha1 "github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/csi"

	"github.com/go-logr/logr"
	snapapi "github.com/kubernetes-csi/external-snapshotter/client/v6/apis/volumesnapshot/v1"
	providerclient "github.com/red-hat-storage/ocs-operator/v4/services/provider/client"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	storageClaimFinalizer  = "storageclaim.ocs.openshift.io"
	storageClaimAnnotation = "ocs.openshift.io/storageclaim"

	// for migration of storageclassclaims to storageclaims
	storageClassClaimFinalizer = "storageclassclaim.ocs.openshift.io"
)

// StorageClaimReconciler reconciles a StorageClaim object
type StorageClaimReconciler struct {
	client.Client
	cache.Cache
	Scheme            *runtime.Scheme
	OperatorNamespace string

	log           logr.Logger
	ctx           context.Context
	storageClient *v1alpha1.StorageClient
	storageClaim  *v1alpha1.StorageClaim
}

// SetupWithManager sets up the controller with the Manager.
func (r *StorageClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueueStorageConsumerRequest := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, obj client.Object) []reconcile.Request {
			annotations := obj.GetAnnotations()
			if _, found := annotations[storageClaimAnnotation]; found {
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Name: obj.GetName(),
					},
				}}
			}
			return []reconcile.Request{}
		})
	enqueueStorageClassClaimRequest := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Name: obj.GetName(),
				},
			}}
		},
	)
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StorageClaim{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
		)).
		Watches(&storagev1.StorageClass{}, enqueueStorageConsumerRequest).
		Watches(&snapapi.VolumeSnapshotClass{}, enqueueStorageConsumerRequest).
		Watches(&v1alpha1.StorageClassClaim{}, enqueueStorageClassClaimRequest, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclaims/finalizers,verbs=update
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclassclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclassclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclassclaims/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotclasses,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the StorageClaim object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *StorageClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	r.log = ctrllog.FromContext(ctx, "StorageClaim", req)
	r.ctx = ctrllog.IntoContext(ctx, r.log)
	r.log.Info("Reconciling StorageClaim.")

	// perform a migration of all existing storageclassclaims to storageclaims and have reconciler bring the
	// resources for newly created storageclaims to the desired state
	if err := r.migrateStorageClassClaims(); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to migrate storageclassclaims")
	}

	// Fetch the StorageClaim instance
	r.storageClaim = &v1alpha1.StorageClaim{}
	r.storageClaim.Name = req.Name

	if err := r.get(r.storageClaim); err != nil {
		if errors.IsNotFound(err) {
			r.log.Info("StorageClaim resource not found. Ignoring since object must be deleted.")
			return reconcile.Result{}, nil
		}
		r.log.Error(err, "Failed to get StorageClaim.")
		return reconcile.Result{}, err
	}

	r.storageClaim.Status.Phase = v1alpha1.StorageClaimInitializing

	if r.storageClaim.Spec.StorageClient == nil {
		storageClientList := &v1alpha1.StorageClientList{}
		if err := r.list(storageClientList); err != nil {
			return reconcile.Result{}, err
		}

		if len(storageClientList.Items) == 0 {
			r.log.Info("No StorageClient resource found.")
			return reconcile.Result{}, fmt.Errorf("no StorageClient found")
		}
		if len(storageClientList.Items) > 1 {
			r.log.Info("Multiple StorageClient resources found, but no storageClient specified.")
			return reconcile.Result{}, fmt.Errorf("multiple StorageClient resources found, but no storageClient specified")
		}
		r.storageClient = &storageClientList.Items[0]
	} else {
		// Fetch the StorageClient instance
		r.storageClient = &v1alpha1.StorageClient{}
		r.storageClient.Name = r.storageClaim.Spec.StorageClient.Name
		r.storageClient.Namespace = r.storageClaim.Spec.StorageClient.Namespace
		if err := r.get(r.storageClient); err != nil {
			r.log.Error(err, "Failed to get StorageClient.")
			return reconcile.Result{}, err
		}
	}

	var result reconcile.Result
	var reconcileError error

	// StorageCluster checks for required fields.
	if r.storageClient.Spec.StorageProviderEndpoint == "" {
		return reconcile.Result{}, fmt.Errorf("no external storage provider endpoint found on the " +
			"StorageClient spec, cannot determine mode")
	}

	result, reconcileError = r.reconcilePhases()

	// Apply status changes to the StorageClaim
	statusError := r.Client.Status().Update(r.ctx, r.storageClaim)
	if statusError != nil {
		r.log.Error(statusError, "Failed to update StorageClaim status.")
	}

	// Reconcile errors have higher priority than status update errors
	if reconcileError != nil {
		return result, reconcileError
	}

	if statusError != nil {
		return result, statusError
	}

	return result, nil
}

func (r *StorageClaimReconciler) reconcilePhases() (reconcile.Result, error) {

	providerClient, err := providerclient.NewProviderClient(
		r.ctx,
		r.storageClient.Spec.StorageProviderEndpoint,
		10*time.Second,
	)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to create provider client: %v", err)
	}

	// Close client-side connections.
	defer providerClient.Close()

	cc := csi.ClusterConfig{
		Client:    r.Client,
		Namespace: r.OperatorNamespace,
		Ctx:       r.ctx,
	}

	if r.storageClaim.GetDeletionTimestamp().IsZero() {

		// TODO: Phases do not have checks at the moment, in order to make them more predictable and less error-prone, at the expense of increased computation cost.
		// Validation phase.
		r.storageClaim.Status.Phase = v1alpha1.StorageClaimValidating

		// If a StorageClass already exists:
		// 	StorageClaim passes validation and is promoted to the configuring phase if:
		//  * the StorageClaim has the same type as the StorageClass.
		// 	* the StorageClaim has no encryption method specified when the type is filesystem.
		// 	* the StorageClaim has a blockpool type and:
		// 		 * the StorageClaim has an encryption method specified.
		// 	  * the StorageClaim has the same encryption method as the StorageClass.
		// 	StorageClaim fails validation and falls back to a failed phase indefinitely (no reconciliation happens).
		existing := &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: r.storageClaim.Name,
			},
		}
		if err = r.get(existing); err == nil {
			sccType := r.storageClaim.Spec.Type
			sccEncryptionMethod := r.storageClaim.Spec.EncryptionMethod
			_, scIsFSType := existing.Parameters["fsName"]
			scEncryptionMethod, scHasEncryptionMethod := existing.Parameters["encryptionMethod"]
			if !((sccType == "sharedfilesystem" && scIsFSType && !scHasEncryptionMethod) ||
				(sccType == "blockpool" && !scIsFSType && sccEncryptionMethod == scEncryptionMethod)) {
				r.log.Error(fmt.Errorf("storageClaim is not compatible with existing StorageClass"),
					"StorageClaim validation failed.")
				r.storageClaim.Status.Phase = v1alpha1.StorageClaimFailed
				return reconcile.Result{}, nil
			}
		} else if err != nil && !errors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("failed to get StorageClass [%v]: %s", existing.ObjectMeta, err)
		}

		// Configuration phase.
		r.storageClaim.Status.Phase = v1alpha1.StorageClaimConfiguring

		updateStorageClaim := false
		// Check if finalizers are present, if not, add them.
		if !contains(r.storageClaim.GetFinalizers(), storageClaimFinalizer) {
			r.log.Info("Finalizer not found for StorageClaim. Adding finalizer.", "StorageClaim", r.storageClaim.Name)
			r.storageClaim.SetFinalizers(append(r.storageClaim.GetFinalizers(), storageClaimFinalizer))
			updateStorageClaim = true
		}
		if addAnnotation(r.storageClaim, storageClientAnnotationKey, client.ObjectKeyFromObject(r.storageClient).String()) {
			updateStorageClaim = true
		}

		if updateStorageClaim {
			if err := r.update(r.storageClaim); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to update StorageClaim %q: %v", r.storageClaim.Name, err)
			}
		}

		// storageClaimStorageType is the storage type of the StorageClaim
		var storageClaimStorageType providerclient.StorageType
		switch r.storageClaim.Spec.Type {
		case "blockpool":
			storageClaimStorageType = providerclient.StorageTypeBlockpool
		case "sharedfilesystem":
			storageClaimStorageType = providerclient.StorageTypeSharedfilesystem
		default:
			return reconcile.Result{}, fmt.Errorf("unsupported storage type: %s", r.storageClaim.Spec.Type)
		}

		// Call the `FulfillStorageClassClaim` service on the provider server with StorageClaim as a request message.
		_, err = providerClient.FulfillStorageClassClaim(
			r.ctx,
			r.storageClient.Status.ConsumerID,
			r.storageClaim.Name,
			storageClaimStorageType,
			r.storageClaim.Spec.StorageProfile,
			r.storageClaim.Spec.EncryptionMethod,
		)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to initiate fulfillment of StorageClaim: %v", err)
		}

		// Call the `GetStorageClassClaimConfig` service on the provider server with StorageClaim as a request message.
		response, err := providerClient.GetStorageClassClaimConfig(
			r.ctx,
			r.storageClient.Status.ConsumerID,
			r.storageClaim.Name,
		)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to get StorageClassClaim config: %v", err)
		}
		resources := response.ExternalResource
		if resources == nil {
			return reconcile.Result{}, fmt.Errorf("no configuration data received")
		}

		var csiClusterConfigEntry = new(csi.ClusterConfigEntry)
		scResponse, err := providerClient.GetStorageConfig(r.ctx, r.storageClient.Status.ConsumerID)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to get StorageConfig: %v", err)
		}
		for _, eResource := range scResponse.ExternalResource {
			if eResource.Kind == "ConfigMap" && eResource.Name == "rook-ceph-mon-endpoints" {
				monitorIps, err := csi.ExtractMonitor(eResource.Data)
				if err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to extract monitor data: %v", err)
				}
				csiClusterConfigEntry.Monitors = append(csiClusterConfigEntry.Monitors, monitorIps...)
			}
		}
		// Go over the received objects and operate on them accordingly.
		for _, resource := range resources {
			data := map[string]string{}
			err = json.Unmarshal(resource.Data, &data)
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to unmarshal StorageClassClaim configuration response: %v", err)
			}

			// Create the received resources, if necessary.
			switch resource.Kind {
			case "Secret":
				if !contains(r.storageClaim.Status.SecretNames, resource.Name) {
					r.storageClaim.Status.SecretNames = append(r.storageClaim.Status.SecretNames, resource.Name)
				}
				secret := &corev1.Secret{}
				secret.Name = resource.Name
				secret.Namespace = r.storageClient.Namespace
				_, err = controllerutil.CreateOrUpdate(r.ctx, r.Client, secret, func() error {
					//cannot own secret here. we need to do get and delete
					if secret.Data == nil {
						secret.Data = map[string][]byte{}
					}
					for k, v := range data {
						secret.Data[k] = []byte(v)
					}
					return nil
				})
				if err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to create or update secret %v: %s", secret, err)
				}
			case "StorageClass":
				csiClusterConfigEntry.ClusterID = r.storageClaim.Name
				var storageClass *storagev1.StorageClass
				data["csi.storage.k8s.io/provisioner-secret-namespace"] = r.storageClient.Namespace
				data["csi.storage.k8s.io/node-stage-secret-namespace"] = r.storageClient.Namespace
				data["csi.storage.k8s.io/controller-expand-secret-namespace"] = r.storageClient.Namespace
				// generate a new clusterID for cephfs subvolumegroup, as
				// storageclaim is clusterscoped resources using its
				// name as the clusterID
				data["clusterID"] = r.storageClaim.Name

				if resource.Name == "cephfs" {
					csiClusterConfigEntry.CephFS = new(csi.CephFSSpec)
					csiClusterConfigEntry.CephFS.SubvolumeGroup = data["subvolumegroupname"]
					// delete groupname from data as its not required in storageclass
					delete(data, "subvolumegroupname")
					storageClass = r.getCephFSStorageClass(data)
				} else if resource.Name == "ceph-rbd" {
					storageClass = r.getCephRBDStorageClass(data)
				}
				addAnnotation(storageClass, storageClaimAnnotation, r.storageClaim.Name)
				err = r.createOrReplaceStorageClass(storageClass)
				if err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to create or update StorageClass: %s", err)
				}
			case "VolumeSnapshotClass":
				var volumeSnapshotClass *snapapi.VolumeSnapshotClass
				data["csi.storage.k8s.io/snapshotter-secret-namespace"] = r.storageClient.Namespace
				// generate a new clusterID for cephfs subvolumegroup, as
				// storageclaim is clusterscoped resources using its
				// name as the clusterID
				data["clusterID"] = r.storageClaim.Name
				if resource.Name == "cephfs" {
					volumeSnapshotClass = r.getCephFSVolumeSnapshotClass(data)
				} else if resource.Name == "ceph-rbd" {
					volumeSnapshotClass = r.getCephRBDVolumeSnapshotClass(data)
				}
				addAnnotation(volumeSnapshotClass, storageClaimAnnotation, r.storageClaim.Name)
				if err := r.createOrReplaceVolumeSnapshotClass(volumeSnapshotClass); err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to create or update VolumeSnapshotClass: %s", err)
				}
			}
		}

		// update monitor configuration for cephcsi
		err = cc.UpdateMonConfigMap(r.storageClaim.Name, r.storageClient.Status.ConsumerID, csiClusterConfigEntry)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update mon configmap: %v", err)
		}
		// Readiness phase.
		// Update the StorageClaim status.
		r.storageClaim.Status.Phase = v1alpha1.StorageClaimReady

		// Initiate deletion phase if the StorageClaim exists.
	} else if r.storageClaim.UID != "" {
		// Deletion phase.
		// Update the StorageClaim status.
		r.storageClaim.Status.Phase = v1alpha1.StorageClaimDeleting

		// Delete StorageClass.
		// Make sure there are no StorageClass consumers left.
		// Check if StorageClass is in use, if yes, then fail.
		// Wait until all PVs using the StorageClass under deletion are removed.
		// Check for any PVs using the StorageClass.
		pvList := corev1.PersistentVolumeList{}
		err := r.list(&pvList)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to list PersistentVolumes: %s", err)
		}
		for i := range pvList.Items {
			pv := &pvList.Items[i]
			if pv.Spec.StorageClassName == r.storageClaim.Name {
				return reconcile.Result{}, fmt.Errorf("StorageClass %s is still in use by one or more PV(s)",
					r.storageClaim.Name)
			}
		}

		// Delete configmap entry for cephcsi
		err = cc.UpdateMonConfigMap(r.storageClaim.Name, r.storageClient.Status.ConsumerID, nil)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update mon configmap: %v", err)
		}

		// Call `RevokeStorageClassClaim` service on the provider server with StorageClaim as a request message.
		// Check if StorageClaim is still exists (it might have been manually removed during the StorageClass
		// removal above).
		_, err = providerClient.RevokeStorageClassClaim(
			r.ctx,
			r.storageClient.Status.ConsumerID,
			r.storageClaim.Name,
		)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to revoke StorageClassClaim: %s", err)
		}

		// Delete secrets created for the StorageClaim.
		for _, secretName := range r.storageClaim.Status.SecretNames {
			secret := &corev1.Secret{}
			secret.Name = secretName
			secret.Namespace = r.storageClient.Namespace
			if err = r.delete(secret); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to delete secret %s/%s: %s", secret.Namespace, secret.Name, err)
			}
		}

		var annotationKey string
		if r.storageClaim.Spec.Type == claimTypeBlockPool {
			annotationKey = defaultStorageClaimBlockPoolAnnotationKey
		} else if r.storageClaim.Spec.Type == claimTypeSharedFs {
			annotationKey = defaultStorageClaimSharedFsAnnotationKey
		}

		// default storageclaim created programmatically
		if r.storageClaim.GetAnnotations()[annotationKey] == "created" {
			storageClient := &v1alpha1.StorageClient{}
			r.storageClient.DeepCopyInto(storageClient)
			if addAnnotation(r.storageClient, annotationKey, "deleted") {
				// using patch as we are concerned only about metadata and doesn't need to be on latest resourceVersion
				if err := r.patch(r.storageClient, client.MergeFrom(storageClient)); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to update annotation %q on storageclient %q: %v", annotationKey, client.ObjectKeyFromObject(r.storageClient), err)
				}
			}
		}

		if contains(r.storageClaim.GetFinalizers(), storageClaimFinalizer) {
			r.storageClaim.Finalizers = remove(r.storageClaim.Finalizers, storageClaimFinalizer)
			if err := r.update(r.storageClaim); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from storageClaim: %s", err)
			}
		}
	}

	return reconcile.Result{}, nil
}

func (r *StorageClaimReconciler) getCephFSStorageClass(data map[string]string) *storagev1.StorageClass {
	pvReclaimPolicy := corev1.PersistentVolumeReclaimDelete
	allowVolumeExpansion := true
	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: r.storageClaim.Name,
			Annotations: map[string]string{
				"description": "Provides RWO and RWX Filesystem volumes",
			},
		},
		ReclaimPolicy:        &pvReclaimPolicy,
		AllowVolumeExpansion: &allowVolumeExpansion,
		Provisioner:          csi.GetCephFSDriverName(),
		Parameters:           data,
	}
	return storageClass
}

func (r *StorageClaimReconciler) getCephRBDStorageClass(data map[string]string) *storagev1.StorageClass {
	pvReclaimPolicy := corev1.PersistentVolumeReclaimDelete
	allowVolumeExpansion := true
	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: r.storageClaim.Name,
			Annotations: map[string]string{
				"description": "Provides RWO Filesystem volumes, and RWO and RWX Block volumes",
			},
		},
		ReclaimPolicy:        &pvReclaimPolicy,
		AllowVolumeExpansion: &allowVolumeExpansion,
		Provisioner:          csi.GetRBDDriverName(),
		Parameters:           data,
	}
	return storageClass
}

func (r *StorageClaimReconciler) getCephFSVolumeSnapshotClass(data map[string]string) *snapapi.VolumeSnapshotClass {
	volumesnapshotclass := &snapapi.VolumeSnapshotClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: r.storageClaim.Name,
		},
		Driver:         csi.GetCephFSDriverName(),
		DeletionPolicy: snapapi.VolumeSnapshotContentDelete,
		Parameters:     data,
	}
	return volumesnapshotclass
}

func (r *StorageClaimReconciler) getCephRBDVolumeSnapshotClass(data map[string]string) *snapapi.VolumeSnapshotClass {
	volumesnapshotclass := &snapapi.VolumeSnapshotClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: r.storageClaim.Name,
		},
		Driver:         csi.GetRBDDriverName(),
		DeletionPolicy: snapapi.VolumeSnapshotContentDelete,
		Parameters:     data,
	}
	return volumesnapshotclass
}

func (r *StorageClaimReconciler) createOrReplaceStorageClass(storageClass *storagev1.StorageClass) error {
	existing := &storagev1.StorageClass{}
	existing.Name = r.storageClaim.Name

	if err := r.own(storageClass); err != nil {
		return fmt.Errorf("failed to own storageclass: %v", err)
	}

	if err := r.get(existing); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get StorageClass: %v", err)
	}

	// If present then compare the existing StorageClass with the received StorageClass, and only proceed if they differ.
	if reflect.DeepEqual(existing.Parameters, storageClass.Parameters) {
		return nil
	}

	// StorageClass already exists, but parameters have changed. Delete the existing StorageClass and create a new one.
	if existing.UID != "" {

		// Since we have to update the existing StorageClass, so we will delete the existing StorageClass and create a new one.
		r.log.Info("StorageClass needs to be updated, deleting it.", "StorageClass", klog.KRef(storageClass.Namespace, existing.Name))

		// Delete the StorageClass.
		err := r.delete(existing)
		if err != nil {
			r.log.Error(err, "Failed to delete StorageClass.", "StorageClass", klog.KRef(storageClass.Namespace, existing.Name))
			return fmt.Errorf("failed to delete StorageClass: %v", err)
		}
	}
	r.log.Info("Creating StorageClass.", "StorageClass", klog.KRef(storageClass.Namespace, existing.Name))
	err := r.Client.Create(r.ctx, storageClass)
	if err != nil {
		return fmt.Errorf("failed to create StorageClass: %v", err)
	}
	return nil
}

func (r *StorageClaimReconciler) get(obj client.Object) error {
	key := client.ObjectKeyFromObject(obj)
	return r.Client.Get(r.ctx, key, obj)
}

func (r *StorageClaimReconciler) update(obj client.Object) error {
	return r.Client.Update(r.ctx, obj)
}

func (r *StorageClaimReconciler) list(obj client.ObjectList, listOptions ...client.ListOption) error {
	return r.Client.List(r.ctx, obj, listOptions...)
}

func (r *StorageClaimReconciler) delete(obj client.Object) error {
	if err := r.Client.Delete(r.ctx, obj); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *StorageClaimReconciler) patch(obj client.Object, patch client.Patch, patchOptions ...client.PatchOption) error {
	return r.Client.Patch(r.ctx, obj, patch, patchOptions...)
}

func (r *StorageClaimReconciler) own(resource metav1.Object) error {
	// Ensure StorageClaim ownership on a resource
	return controllerutil.SetOwnerReference(r.storageClaim, resource, r.Scheme)
}

// addAnnotation adds an annotation to a resource metadata, returns true if added else false
func addAnnotation(obj metav1.Object, key string, value string) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
		obj.SetAnnotations(annotations)
	}
	if oldValue, exist := annotations[key]; !exist || oldValue != value {
		annotations[key] = value
		return true
	}
	return false
}

func (r *StorageClaimReconciler) createOrReplaceVolumeSnapshotClass(volumeSnapshotClass *snapapi.VolumeSnapshotClass) error {
	existing := &snapapi.VolumeSnapshotClass{}
	existing.Name = r.storageClaim.Name

	if err := r.own(volumeSnapshotClass); err != nil {
		return fmt.Errorf("failed to own volumesnapshotclass: %v", err)
	}

	if err := r.get(existing); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get VolumeSnapshotClass: %v", err)
	}

	// If present then compare the existing VolumeSnapshotClass parameters with
	// the received VolumeSnapshotClass parameters, and only proceed if they differ.
	if reflect.DeepEqual(existing.Parameters, volumeSnapshotClass.Parameters) {
		return nil
	}

	// VolumeSnapshotClass already exists, but parameters have changed. Delete the existing VolumeSnapshotClass and create a new one.
	if existing.UID != "" {
		// Since we have to update the existing VolumeSnapshotClass, so we will delete the existing VolumeSnapshotClass and create a new one.
		r.log.Info("VolumeSnapshotClass needs to be updated, deleting it.", "Name", existing.Name)

		// Delete the VolumeSnapshotClass.
		if err := r.delete(existing); err != nil {
			r.log.Error(err, "Failed to delete VolumeSnapshotClass.", "Name", existing.Name)
			return fmt.Errorf("failed to delete VolumeSnapshotClass: %v", err)
		}
	}
	r.log.Info("Creating VolumeSnapshotClass.", "Name", existing.Name)
	if err := r.Client.Create(r.ctx, volumeSnapshotClass); err != nil {
		return fmt.Errorf("failed to create VolumeSnapshotClass: %v", err)
	}
	return nil
}

func (r *StorageClaimReconciler) migrateStorageClassClaims() error {
	storageClassClaimsList := &v1alpha1.StorageClassClaimList{}
	if err := r.list(storageClassClaimsList); err != nil {
		return fmt.Errorf("failed to list storageclassclaims")
	}

	for idx := range storageClassClaimsList.Items {
		storageClassClaim := &storageClassClaimsList.Items[idx]
		storageClaim := &v1alpha1.StorageClaim{}
		storageClaim.Name = storageClassClaim.Name

		// migrate storageclassclaim to storageclaim
		_, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, storageClaim, func() error {
			storageClaim.Spec.Type = storageClassClaim.Spec.Type
			storageClaim.Spec.EncryptionMethod = storageClassClaim.Spec.EncryptionMethod
			storageClaim.Spec.StorageProfile = storageClassClaim.Spec.StorageProfile
			storageClaim.Spec.StorageClient = storageClassClaim.Spec.StorageClient.DeepCopy()
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to copy spec from storageclassclaims to storageclaims: %v", err)
		}

		// remove finalizer on existing storageclassclaim
		finalizerUpdated := controllerutil.RemoveFinalizer(storageClassClaim, storageClassClaimFinalizer)
		if finalizerUpdated {
			if err := r.update(storageClassClaim); err != nil {
				return fmt.Errorf("failed to remove finalizer on storageclassclaim %q: %v", storageClassClaim.Name, err)
			}
		}

		// migration is successful delete the storageclassclaim
		if err := r.delete(storageClassClaim); err != nil {
			return fmt.Errorf("failed to delete storageclassclaim %q: %v", storageClassClaim.Name, err)
		}

		r.log.Info(fmt.Sprintf("Successfully migrated storageclassclaim %q to storageclass %q", storageClassClaim.Name, storageClaim.Name))
	}

	return nil
}
