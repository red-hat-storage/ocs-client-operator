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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	v1alpha1 "github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	snapapi "github.com/kubernetes-csi/external-snapshotter/client/v6/apis/volumesnapshot/v1"
	ramenv1alpha1 "github.com/ramendr/ramen/api/v1alpha1"
	providerclient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	storageClaimFinalizer       = "storageclaim.ocs.openshift.io"
	storageClaimAnnotation      = "ocs.openshift.io/storageclaim"
	keyRotationAnnotation       = "keyrotation.csiaddons.openshift.io/schedule"
	keyRotationEnableAnnotation = "keyrotation.csiaddons.openshift.io/enable"

	pvClusterIDIndexName  = "index:persistentVolumeClusterID"
	vscClusterIDIndexName = "index:volumeSnapshotContentCSIDriver"

	drClusterConfigCRDName = "drclusterconfigs.ramendr.openshift.io"
)

// StorageClaimReconciler reconciles a StorageClaim object
type StorageClaimReconciler struct {
	client.Client
	cache.Cache
	Scheme            *runtime.Scheme
	OperatorNamespace string
	AvailableCrds     map[string]bool

	log              logr.Logger
	ctx              context.Context
	storageClient    *v1alpha1.StorageClient
	storageClaim     *v1alpha1.StorageClaim
	storageClaimHash string
}

// SetupWithManager sets up the controller with the Manager.
func (r *StorageClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	csiDrivers := []string{templates.RBDDriverName, templates.CephFsDriverName}
	if err := mgr.GetCache().IndexField(ctx, &corev1.PersistentVolume{}, pvClusterIDIndexName, func(o client.Object) []string {
		pv := o.(*corev1.PersistentVolume)
		if pv != nil &&
			pv.Spec.CSI != nil &&
			slices.Contains(csiDrivers, pv.Spec.CSI.Driver) &&
			pv.Spec.CSI.VolumeAttributes["clusterID"] != "" {
			return []string{pv.Spec.CSI.VolumeAttributes["clusterID"]}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("unable to set up FieldIndexer for PV cluster id: %v", err)
	}

	if err := mgr.GetCache().IndexField(ctx, &snapapi.VolumeSnapshotContent{}, vscClusterIDIndexName, func(o client.Object) []string {
		vsc := o.(*snapapi.VolumeSnapshotContent)
		if vsc != nil &&
			slices.Contains(csiDrivers, vsc.Spec.Driver) &&
			vsc.Status != nil &&
			vsc.Status.SnapshotHandle != nil {
			parts := strings.Split(*vsc.Status.SnapshotHandle, "-")
			if len(parts) == 9 {
				// second entry in the volumeID is clusterID which is unique across the cluster
				return []string{parts[2]}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("unable to set up FieldIndexer for VSC csi driver name: %v", err)
	}

	generationChangePredicate := predicate.GenerationChangedPredicate{}
	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StorageClaim{}, builder.WithPredicates(generationChangePredicate)).
		Owns(&storagev1.StorageClass{}).
		Owns(&snapapi.VolumeSnapshotClass{}).
		Watches(
			&extv1.CustomResourceDefinition{},
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(
				utils.NamePredicate(drClusterConfigCRDName),
				utils.CrdCreateAndDeletePredicate(&r.log, drClusterConfigCRDName, r.AvailableCrds[drClusterConfigCRDName]),
			),
			builder.OnlyMetadata,
		).
		Owns(&csiopv1a1.ClientProfile{}, builder.WithPredicates(generationChangePredicate))

	if r.AvailableCrds[drClusterConfigCRDName] {
		bldr = bldr.Owns(&ramenv1alpha1.DRClusterConfig{}, builder.WithPredicates(generationChangePredicate))
	}

	return bldr.Complete(r)
}

//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclaims/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotclasses,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotcontents,verbs=get;list;watch
//+kubebuilder:rbac:groups=csi.ceph.io,resources=clientprofiles,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=ramendr.openshift.io,resources=drclusterconfigs,verbs=get;list;update;create;watch;delete

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

	crd := &metav1.PartialObjectMetadata{}
	crd.SetGroupVersionKind(extv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	crd.Name = drClusterConfigCRDName
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(crd), crd); client.IgnoreNotFound(err) != nil {
		r.log.Error(err, "Failed to get CRD", "CRD", drClusterConfigCRDName)
		return reconcile.Result{}, err
	}
	utils.AssertEqual(r.AvailableCrds[drClusterConfigCRDName], crd.UID != "", utils.ExitCodeThatShouldRestartTheProcess)

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

	r.storageClaimHash = utils.GetMD5Hash(r.storageClaim.Name)
	r.storageClaim.Status.Phase = v1alpha1.StorageClaimInitializing

	if r.storageClaim.Spec.StorageClient == "" {
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
		r.storageClient.Name = r.storageClaim.Spec.StorageClient
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
		return reconcile.Result{}, fmt.Errorf("failed to create provider client with endpoint %v: %v", r.storageClient.Spec.StorageProviderEndpoint, err)
	}

	// Close client-side connections.
	defer providerClient.Close()

	if r.storageClaim.GetDeletionTimestamp().IsZero() {

		// TODO: Phases do not have checks at the moment, in order to make them more predictable and less error-prone, at the expense of increased computation cost.
		// Validation phase.
		r.storageClaim.Status.Phase = v1alpha1.StorageClaimValidating

		// If a StorageClass already exists:
		// 	StorageClaim passes validation and is promoted to the configuring phase if:
		//  * the StorageClaim has the same type as the StorageClass.
		// 	* the StorageClaim has no encryption method specified when the type is filesystem.
		// 	* the StorageClaim has a block type and:
		// 		 * the StorageClaim has an encryption method specified.
		// 	  * the StorageClaim has the same encryption method as the StorageClass.
		// 	StorageClaim fails validation and falls back to a failed phase indefinitely (no reconciliation happens).
		existing := &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: r.storageClaim.Name,
			},
		}
		claimType := strings.ToLower(r.storageClaim.Spec.Type)
		if err = r.get(existing); err == nil {
			sccEncryptionMethod := r.storageClaim.Spec.EncryptionMethod
			_, scIsFSType := existing.Parameters["fsName"]
			scEncryptionMethod, scHasEncryptionMethod := existing.Parameters["encryptionMethod"]
			if !((claimType == "sharedfile" && scIsFSType && !scHasEncryptionMethod) ||
				(claimType == "block" && !scIsFSType && sccEncryptionMethod == scEncryptionMethod)) {
				r.log.Error(fmt.Errorf("storageClaim is not compatible with existing StorageClass"),
					"StorageClaim validation failed.")
				r.storageClaim.Status.Phase = v1alpha1.StorageClaimFailed
				return reconcile.Result{}, nil
			}
		} else if !errors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("failed to get StorageClass [%v]: %s", existing.ObjectMeta, err)
		}

		// Configuration phase.
		r.storageClaim.Status.Phase = v1alpha1.StorageClaimConfiguring

		// Check if finalizers are present, if not, add them.
		if controllerutil.AddFinalizer(r.storageClaim, storageClaimFinalizer) {
			if err := r.update(r.storageClaim); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to update StorageClaim %q: %v", r.storageClaim.Name, err)
			}
		}

		// storageClaimStorageType is the storage type of the StorageClaim
		var storageClaimStorageType providerclient.StorageType
		switch claimType {
		case "block":
			storageClaimStorageType = providerclient.StorageTypeBlock
		case "sharedfile":
			storageClaimStorageType = providerclient.StorageTypeSharedFile
		default:
			return reconcile.Result{}, fmt.Errorf("unsupported storage type: %s", claimType)
		}

		// Call the `FulfillStorageClaim` service on the provider server with StorageClaim as a request message.
		_, err = providerClient.FulfillStorageClaim(
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

		// Call the `GetStorageClaimConfig` service on the provider server with StorageClaim as a request message.
		response, err := providerClient.GetStorageClaimConfig(
			r.ctx,
			r.storageClient.Status.ConsumerID,
			r.storageClaim.Name,
		)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to get StorageClaim config: %v", err)
		}
		resources := response.ExternalResource
		if resources == nil {
			return reconcile.Result{}, fmt.Errorf("no configuration data received")
		}

		// Go over the received objects and operate on them accordingly.
		for _, resource := range resources {

			// Create the received resources, if necessary.
			switch resource.Kind {
			case "Secret":
				data := map[string]string{}
				err = json.Unmarshal(resource.Data, &data)
				if err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to unmarshal StorageClaim configuration response: %v", err)
				}
				secret := &corev1.Secret{}
				secret.Name = resource.Name
				secret.Namespace = r.OperatorNamespace
				_, err = controllerutil.CreateOrUpdate(r.ctx, r.Client, secret, func() error {
					// cluster scoped resource owning namespace scoped resource which allows garbage collection
					if err := r.own(secret); err != nil {
						return fmt.Errorf("failed to own secret: %v", err)
					}

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
				data := map[string]string{}
				err = json.Unmarshal(resource.Data, &data)
				if err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to unmarshal StorageClaim configuration response: %v", err)
				}
				// we are now using clientprofile from csi-operator for getting this info.
				// until provider stops sending this info we'll just need to drop the field
				// we'll make changes to provider at some version when all clients are dropping this field
				delete(data, "radosnamespace")
				delete(data, "subvolumegroupname")

				var storageClass *storagev1.StorageClass
				data["csi.storage.k8s.io/provisioner-secret-namespace"] = r.OperatorNamespace
				data["csi.storage.k8s.io/node-stage-secret-namespace"] = r.OperatorNamespace
				data["csi.storage.k8s.io/controller-expand-secret-namespace"] = r.OperatorNamespace
				data["clusterID"] = r.storageClaimHash

				if resource.Name == "cephfs" {
					storageClass = r.getCephFSStorageClass(data)
				} else if resource.Name == "ceph-rbd" {
					storageClass = r.getCephRBDStorageClass(data)

					if enable, ok := r.storageClaim.GetAnnotations()[keyRotationEnableAnnotation]; ok && enable != "true" {
						utils.AddAnnotation(storageClass, keyRotationEnableAnnotation, "false")
					}
				}
				utils.AddAnnotation(storageClass, storageClaimAnnotation, r.storageClaim.Name)
				err = r.createOrReplaceStorageClass(storageClass)
				if err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to create or update StorageClass: %s", err)
				}
			case "VolumeSnapshotClass":
				data := map[string]string{}
				err = json.Unmarshal(resource.Data, &data)
				if err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to unmarshal StorageClaim configuration response: %v", err)
				}
				var volumeSnapshotClass *snapapi.VolumeSnapshotClass
				data["csi.storage.k8s.io/snapshotter-secret-namespace"] = r.OperatorNamespace
				// generate a new clusterID for cephfs subvolumegroup, as
				// storageclaim is clusterscoped resources using its
				// hash as the clusterID
				data["clusterID"] = r.storageClaimHash
				if resource.Name == "cephfs" {
					volumeSnapshotClass = r.getCephFSVolumeSnapshotClass(data)
				} else if resource.Name == "ceph-rbd" {
					volumeSnapshotClass = r.getCephRBDVolumeSnapshotClass(data)
				}
				utils.AddAnnotation(volumeSnapshotClass, storageClaimAnnotation, r.storageClaim.Name)
				if err := r.createOrReplaceVolumeSnapshotClass(volumeSnapshotClass); err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to create or update VolumeSnapshotClass: %s", err)
				}
			case "ClientProfile":
				clientProfile := &csiopv1a1.ClientProfile{}
				clientProfile.Name = r.storageClaimHash
				clientProfile.Namespace = r.OperatorNamespace
				if _, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, clientProfile, func() error {
					if err := r.own(clientProfile); err != nil {
						return fmt.Errorf("failed to own clientProfile resource: %v", err)
					}
					if err := json.Unmarshal(resource.Data, &clientProfile.Spec); err != nil {
						return fmt.Errorf("failed to unmarshall clientProfile spec: %v", err)
					}
					clientProfile.Spec.CephConnectionRef = corev1.LocalObjectReference{
						Name: r.storageClient.Name,
					}
					return nil
				}); err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to reconcile clientProfile: %v", err)
				}
			}
		}

		// Readiness phase.
		// Update the StorageClaim status.
		r.storageClaim.Status.Phase = v1alpha1.StorageClaimReady

		// Initiate deletion phase if the StorageClaim exists.
	} else if r.storageClaim.UID != "" {
		// Deletion phase.
		// Update the StorageClaim status.
		r.storageClaim.Status.Phase = v1alpha1.StorageClaimDeleting

		if exist, err := r.hasPersistentVolumes(); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to verify persistentvolumes dependent on storageclaim %q: %v", r.storageClaim.Name, err)
		} else if exist {
			return reconcile.Result{}, fmt.Errorf("one or more persistentvolumes exist that are dependent on storageclaim %s", r.storageClaim.Name)
		}

		if exist, err := r.hasVolumeSnapshotContents(); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to verify volumesnapshotcontents dependent on storageclaim %q: %v", r.storageClaim.Name, err)
		} else if exist {
			return reconcile.Result{}, fmt.Errorf("one or more volumesnapshotcontents exist that are dependent on storageclaim %s", r.storageClaim.Name)
		}

		// Call `RevokeStorageClaim` service on the provider server with StorageClaim as a request message.
		// Check if StorageClaim is still exists (it might have been manually removed during the StorageClass
		// removal above).
		_, err = providerClient.RevokeStorageClaim(
			r.ctx,
			r.storageClient.Status.ConsumerID,
			r.storageClaim.Name,
		)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to revoke StorageClaim: %s", err)
		}

		if controllerutil.RemoveFinalizer(r.storageClaim, storageClaimFinalizer) {
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
		Provisioner:          templates.CephFsDriverName,
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
				"reclaimspace.csiaddons.openshift.io/schedule": "@weekly",
			},
		},
		ReclaimPolicy:        &pvReclaimPolicy,
		AllowVolumeExpansion: &allowVolumeExpansion,
		Provisioner:          templates.RBDDriverName,
		Parameters:           data,
	}

	if r.storageClaim.Spec.EncryptionMethod != "" {
		utils.AddAnnotation(storageClass, keyRotationAnnotation, utils.CronScheduleWeekly)
	}
	return storageClass
}

func (r *StorageClaimReconciler) getCephFSVolumeSnapshotClass(data map[string]string) *snapapi.VolumeSnapshotClass {
	volumesnapshotclass := &snapapi.VolumeSnapshotClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: r.storageClaim.Name,
		},
		Driver:         templates.CephFsDriverName,
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
		Driver:         templates.RBDDriverName,
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
	if reflect.DeepEqual(existing.Parameters, storageClass.Parameters) &&
		reflect.DeepEqual(existing.Annotations, storageClass.Annotations) {
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

func (r *StorageClaimReconciler) own(resource metav1.Object) error {
	return controllerutil.SetControllerReference(r.storageClaim, resource, r.Scheme)
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

func (r *StorageClaimReconciler) hasPersistentVolumes() (bool, error) {
	pvList := &corev1.PersistentVolumeList{}
	if err := r.list(pvList, client.MatchingFields{pvClusterIDIndexName: r.storageClaimHash}, client.Limit(1)); err != nil {
		return false, fmt.Errorf("failed to list persistent volumes: %v", err)
	}

	if len(pvList.Items) != 0 {
		r.log.Info(fmt.Sprintf("PersistentVolumes referring storageclaim %q exists", r.storageClaim.Name))
		return true, nil
	}

	return false, nil
}

func (r *StorageClaimReconciler) hasVolumeSnapshotContents() (bool, error) {
	vscList := &snapapi.VolumeSnapshotContentList{}
	if err := r.list(vscList, client.MatchingFields{vscClusterIDIndexName: r.storageClaimHash}); err != nil {
		return false, fmt.Errorf("failed to list volume snapshot content resources: %v", err)
	}

	if len(vscList.Items) != 0 {
		r.log.Info(fmt.Sprintf("VolumeSnapshotContent referring storageclaim %q exists", r.storageClaim.Name))
		return true, nil
	}

	return false, nil
}
