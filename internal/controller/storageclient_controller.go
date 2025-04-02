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
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	replicationv1a1 "github.com/csi-addons/kubernetes-csi-addons/api/replication.storage/v1alpha1"
	"github.com/go-logr/logr"
	groupsnapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumegroupsnapshot/v1beta1"
	snapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	nbv1 "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	quotav1 "github.com/openshift/api/quota/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	providerpb "github.com/red-hat-storage/ocs-operator/services/provider/api/v4"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// grpcCallNames
	OnboardConsumer  = "OnboardConsumer"
	OffboardConsumer = "OffboardConsumer"
	GetStorageConfig = "GetStorageConfig"

	storageClientNameLabel            = "ocs.openshift.io/storageclient.name"
	storageClientFinalizer            = "storageclient.ocs.openshift.io"
	storageClientDefaultAnnotationKey = "ocs.openshift.io/storageclient.default"

	// indexes for caching
	ownerUIDIndexName      = "index:ownerUID"
	pvClusterIDIndexName   = "index:persistentVolumeClusterID"
	vscClusterIDIndexName  = "index:volumeSnapshotContentCSIDriver"
	vgscClusterIDIndexName = "index:volumeGroupSnapshotContentCSIDriver"

	csvPrefix = "ocs-client-operator"

	VolumeGroupSnapshotClassCrdName = "volumegroupsnapshotclasses.groupsnapshot.storage.k8s.io"
)

var (
	csiDrivers = []string{
		templates.RBDDriverName,
		templates.CephFsDriverName,
	}
)

// StorageClientReconciler reconciles a StorageClient object
type StorageClientReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	OperatorNamespace string

	cache            cache.Cache
	controller       controller.Controller
	crdsBeingWatched sync.Map
}

type storageClientReconcile struct {
	*StorageClientReconciler

	ctx           context.Context
	log           logr.Logger
	storageClient v1alpha1.StorageClient
}

// SetupWithManager sets up the controller with the Manager.
func (r *StorageClientReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
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
	if err := mgr.GetCache().IndexField(ctx, &csiopv1a1.ClientProfile{}, ownerUIDIndexName, func(obj client.Object) []string {
		refs := obj.GetOwnerReferences()
		owners := []string{}
		for i := range refs {
			owners = append(owners, string(refs[i].UID))
		}
		return owners
	}); err != nil {
		return fmt.Errorf("unable to set up FieldIndexer for client profile owner: %v", err)
	}
	generationChangePredicate := predicate.GenerationChangedPredicate{}
	enqueueStorageClients := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, _ client.Object) []ctrl.Request {
			storageClients := &v1alpha1.StorageClientList{}
			if err := r.Client.List(ctx, storageClients); err != nil {
				return []ctrl.Request{}
			}
			requests := make([]ctrl.Request, len(storageClients.Items))
			for idx := range storageClients.Items {
				requests[idx] = ctrl.Request{
					NamespacedName: client.ObjectKeyFromObject(&storageClients.Items[idx]),
				}
			}
			return requests
		},
	)
	controller, err := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StorageClient{}).
		Owns(&batchv1.CronJob{}).
		Owns(&quotav1.ClusterResourceQuota{}, builder.WithPredicates(generationChangePredicate)).
		Owns(&nbv1.NooBaa{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&corev1.Secret{}).
		Owns(&csiopv1a1.CephConnection{}, builder.WithPredicates(generationChangePredicate)).
		Owns(&csiopv1a1.ClientProfileMapping{}, builder.WithPredicates(generationChangePredicate)).
		Owns(&storagev1.StorageClass{}).
		Owns(&snapapi.VolumeSnapshotClass{}).
		Owns(&replicationv1a1.VolumeReplicationClass{}, builder.WithPredicates(generationChangePredicate)).
		Owns(&csiopv1a1.ClientProfile{}, builder.WithPredicates(generationChangePredicate)).
		Watches(
			&extv1.CustomResourceDefinition{},
			enqueueStorageClients,
			builder.WithPredicates(
				predicate.NewPredicateFuncs(func(obj client.Object) bool {
					_, ok := r.crdsBeingWatched.Load(obj.GetName())
					return ok
				}),
				utils.EventTypePredicate(true, false, false, false),
			),
			builder.OnlyMetadata,
		).
		Build(r)

	r.controller = controller
	r.cache = mgr.GetCache()
	r.crdsBeingWatched.Store(VolumeGroupSnapshotClassCrdName, false)

	return err
}

//+kubebuilder:rbac:groups=quota.openshift.io,resources=clusterresourcequotas,verbs=get;list;watch;create;update
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients/finalizers,verbs=update
//+kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;create;update;watch;delete
//+kubebuilder:rbac:groups=operators.coreos.com,resources=clusterserviceversions,verbs=get;list;watch
//+kubebuilder:rbac:groups=csi.ceph.io,resources=cephconnections,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=noobaa.io,resources=noobaas,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=csi.ceph.io,resources=clientprofilemappings,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=csi.ceph.io,resources=clientprofiles,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=replication.storage.openshift.io,resources=volumereplicationclasses,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotclasses,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotcontents,verbs=get;list;watch
//+kubebuilder:rbac:groups=groupsnapshot.storage.k8s.io,resources=volumegroupsnapshotclasses,verbs=get;list;watch;create;delete;
//+kubebuilder:rbac:groups=groupsnapshot.storage.k8s.io,resources=volumegroupsnapshotcontents,verbs=get;list;watch

func (r *StorageClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	handler := storageClientReconcile{StorageClientReconciler: r}
	return handler.reconcile(ctx, req)
}

func (r *storageClientReconcile) reconcile(ctx context.Context, req ctrl.Request) (reconcile.Result, error) {
	r.log = ctrl.LoggerFrom(ctx)
	r.ctx = ctx
	r.storageClient.Name = req.Name

	r.log.Info("Starting reconcile iteration for StorageClient", "req", req)
	if err := r.get(&r.storageClient); err != nil {
		if kerrors.IsNotFound(err) {
			r.log.Info("StorageClient resource not found. Ignoring since object must be deleted.")
			return reconcile.Result{}, nil
		}
		r.log.Error(err, "Failed to get StorageClient.")
		return reconcile.Result{}, fmt.Errorf("failed to get StorageClient: %v", err)
	}
	if r.storageClient.Status.Phase == v1alpha1.StorageClientFailed {
		return reconcile.Result{}, nil
	}

	result, reconcileErr := r.reconcilePhases()

	statusErr := r.Client.Status().Update(r.ctx, &r.storageClient)
	if statusErr != nil {
		r.log.Error(statusErr, "Failed to update StorageClient status.")
	}
	if reconcileErr != nil {
		return reconcile.Result{}, reconcileErr
	} else if statusErr != nil {
		return reconcile.Result{}, statusErr
	}

	return result, nil
}

func (r *storageClientReconcile) reconcileDynamicWatches() error {
	if watchExists, foundCrd := r.crdsBeingWatched.Load(VolumeGroupSnapshotClassCrdName); !foundCrd || watchExists.(bool) {
		return nil
	}

	crd := &metav1.PartialObjectMetadata{}
	crd.SetGroupVersionKind(extv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	crd.Name = VolumeGroupSnapshotClassCrdName
	if err := r.get(crd); client.IgnoreNotFound(err) != nil {
		return err
	}
	// CRD doesn't exist in the cluster
	if crd.UID == "" {
		return nil
	}

	// establish a watch
	if err := r.controller.Watch(
		source.Kind(
			r.cache,
			client.Object(&groupsnapapi.VolumeGroupSnapshotContent{}),
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []ctrl.Request {
				owner := metav1.GetControllerOf(o)
				if owner != nil &&
					owner.Kind == "StorageClient" &&
					owner.APIVersion == v1alpha1.GroupVersion.String() {
					return []ctrl.Request{{NamespacedName: types.NamespacedName{Name: owner.Name}}}
				}
				return nil
			}),
		),
	); err != nil {
		return fmt.Errorf("failed to setup dynamic watch on %s: %v", crd.Name, err)
	}

	// add an index
	if err := r.cache.IndexField(r.ctx, &groupsnapapi.VolumeGroupSnapshotContent{}, vgscClusterIDIndexName, func(o client.Object) []string {
		vgsc := o.(*groupsnapapi.VolumeGroupSnapshotContent)
		if vgsc != nil &&
			slices.Contains(csiDrivers, vgsc.Spec.Driver) &&
			vgsc.Status != nil &&
			vgsc.Status.VolumeGroupSnapshotHandle != nil {
			parts := strings.Split(*vgsc.Status.VolumeGroupSnapshotHandle, "-")
			if len(parts) == 9 {
				// second entry in the volumeID is clusterID which is unique across the cluster
				return []string{parts[2]}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("unable to set up FieldIndexer for VGSC csi driver name: %v", err)
	}
	r.crdsBeingWatched.Store(VolumeGroupSnapshotClassCrdName, true)
	return nil
}

func (r *storageClientReconcile) reconcilePhases() (ctrl.Result, error) {
	if err := r.reconcileDynamicWatches(); err != nil {
		return reconcile.Result{}, err
	}

	externalClusterClient, err := r.newExternalClusterClient()
	if err != nil {
		return reconcile.Result{}, err
	}
	defer externalClusterClient.Close()

	// deletion phase
	if !r.storageClient.GetDeletionTimestamp().IsZero() {
		return r.deletionPhase(externalClusterClient)
	}

	updateStorageClient := false
	storageClients := &v1alpha1.StorageClientList{}
	if err := r.list(storageClients); err != nil {
		r.log.Error(err, "unable to list storage clients")
		return ctrl.Result{}, err
	}
	if len(storageClients.Items) == 1 && storageClients.Items[0].Name == r.storageClient.Name {
		if utils.AddAnnotation(&r.storageClient, storageClientDefaultAnnotationKey, "true") {
			updateStorageClient = true
		}
	}
	if controllerutil.AddFinalizer(&r.storageClient, storageClientFinalizer) {
		r.storageClient.Status.Phase = v1alpha1.StorageClientInitializing
		r.log.Info("Finalizer not found for StorageClient. Adding finalizer.", "StorageClient", r.storageClient.Name)
		updateStorageClient = true
	}
	if updateStorageClient {
		if err := r.update(&r.storageClient); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update StorageClient: %v", err)
		}
	}

	if r.storageClient.Status.ConsumerID == "" {
		if err := r.onboardConsumer(externalClusterClient); err != nil {
			return reconcile.Result{}, err
		}
	}

	if res, err := r.reconcileClientStatusReporterJob(); err != nil {
		return res, err
	}

	storageClientResponse, err := externalClusterClient.GetStorageConfig(r.ctx, r.storageClient.Status.ConsumerID)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get StorageConfig: %v", err)
	}

	if storageClientResponse.SystemAttributes != nil {
		r.storageClient.Status.InMaintenanceMode = storageClientResponse.SystemAttributes.SystemInMaintenanceMode
	}

	for _, eResource := range storageClientResponse.ExternalResource {
		var err error
		// Create the received resources, if necessary.
		switch eResource.Kind {
		case "ClusterResourceQuota":
			var clusterResourceQuotaSpec *quotav1.ClusterResourceQuotaSpec
			if err := json.Unmarshal(eResource.Data, &clusterResourceQuotaSpec); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to unmarshall clusterResourceQuotaSpec error: %v", err)
			}
			if err := r.reconcileClusterResourceQuota(clusterResourceQuotaSpec); err != nil {
				return reconcile.Result{}, err
			}
		case "CephConnection":
			cephConnection := &csiopv1a1.CephConnection{}
			cephConnection.Name = eResource.Name
			cephConnection.Namespace = r.OperatorNamespace
			if err := r.createOrUpdate(cephConnection, func() error {
				if err := r.own(cephConnection); err != nil {
					return fmt.Errorf("failed to own cephConnection resource: %v", err)
				}
				if err := json.Unmarshal(eResource.Data, &cephConnection.Spec); err != nil {
					return fmt.Errorf("failed to unmarshall cephConnectionSpec: %v", err)
				}
				return nil
			}); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to reconcile cephConnection: %v", err)
			}
		case "ClientProfileMapping":
			clientProfileMapping := &csiopv1a1.ClientProfileMapping{}
			clientProfileMapping.Name = eResource.Name
			clientProfileMapping.Namespace = r.OperatorNamespace
			if _, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, clientProfileMapping, func() error {
				if err := r.own(clientProfileMapping); err != nil {
					return fmt.Errorf("failed to own clientProfileMapping resource: %v", err)
				}
				if err := json.Unmarshal(eResource.Data, &clientProfileMapping.Spec); err != nil {
					return fmt.Errorf("failed to unmarshall clientProfileMapping spec: %v", err)
				}
				// TODO: This is a temporary solution till we have a single clientProfile for all storageClass
				// sent from Provider
				clientProfileHash := utils.GetMD5Hash(fmt.Sprintf("%s-ceph-rbd", r.storageClient.Name))
				for i := range clientProfileMapping.Spec.Mappings {
					clientProfileMapping.Spec.Mappings[i].LocalClientProfile = clientProfileHash
					clientProfileMapping.Spec.Mappings[i].RemoteClientProfile = clientProfileHash
				}
				return nil
			}); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to reconcile clientProfileMapping: %v", err)
			}
		case "Secret":
			data := map[string][]byte{}
			if err := json.Unmarshal(eResource.Data, &data); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to unmarshall secret: %v", err)
			}
			secret := &corev1.Secret{}
			secret.Name = eResource.Name
			secret.Namespace = r.OperatorNamespace
			_, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, secret, func() error {
				removeStorageClaimAsOwner(secret)
				if err := r.own(secret); err != nil {
					return err
				}
				secret.Data = data
				return nil
			})
			if err != nil {
				return reconcile.Result{}, fmt.Errorf(
					"failed to create or update secret %v: %v",
					client.ObjectKeyFromObject(secret),
					err,
				)
			}
		case "Noobaa":
			noobaaSpec := &nbv1.NooBaaSpec{}
			if err := json.Unmarshal(eResource.Data, &noobaaSpec); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to unmarshall noobaa spec data: %v", err)
			}
			nb := &nbv1.NooBaa{}
			nb.Name = eResource.Name
			nb.Namespace = r.OperatorNamespace

			_, err = controllerutil.CreateOrUpdate(r.ctx, r.Client, nb, func() error {
				if err := r.own(nb); err != nil {
					return err
				}
				utils.AddAnnotation(nb, "remote-client-noobaa", "true")
				noobaaSpec.JoinSecret.Namespace = r.OperatorNamespace
				nb.Spec = *noobaaSpec
				return nil
			})
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to create remote noobaa: %v", err)
			}
		case "StorageClass":
			err = r.EnsureClusterScopedResource(&storagev1.StorageClass{}, eResource, true)
		case "VolumeSnapshotClass":
			err = r.EnsureClusterScopedResource(&snapapi.VolumeSnapshotClass{}, eResource, true)
		case "VolumeGroupSnapshotClass":
			if val, _ := r.crdsBeingWatched.Load(VolumeGroupSnapshotClassCrdName); !val.(bool) {
				continue
			}
			err = r.EnsureClusterScopedResource(&groupsnapapi.VolumeGroupSnapshotClass{}, eResource, true)
		case "VolumeReplicationClass":
			err = r.EnsureClusterScopedResource(&replicationv1a1.VolumeReplicationClass{}, eResource, true)
		case "ClientProfile":
			clientProfile := &csiopv1a1.ClientProfile{}
			clientProfile.Name = eResource.Name
			clientProfile.Namespace = r.OperatorNamespace
			if err := r.createOrUpdate(clientProfile, func() error {
				if err := r.own(clientProfile); err != nil {
					return fmt.Errorf("failed to own clientProfile resource: %v", err)
				}
				if err := json.Unmarshal(eResource.Data, &clientProfile.Spec); err != nil {
					return fmt.Errorf("failed to unmarshall clientProfile: %v", err)
				}
				return nil
			}); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to reconcile clientProfile: %v", err)
			}
		}
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	if utils.AddAnnotation(&r.storageClient, utils.DesiredConfigHashAnnotationKey, storageClientResponse.DesiredConfigHash) {
		if err := r.update(&r.storageClient); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update StorageClient with desired config hash annotation: %v", err)
		}
	}

	return reconcile.Result{}, nil
}

func (r *storageClientReconcile) reconcileClusterResourceQuota(spec *quotav1.ClusterResourceQuotaSpec) error {
	clusterResourceQuota := &quotav1.ClusterResourceQuota{}
	clusterResourceQuota.Name = utils.GetClusterResourceQuotaName(r.storageClient.Name)
	_, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, clusterResourceQuota, func() error {

		if err := r.own(clusterResourceQuota); err != nil {
			return err
		}

		clusterResourceQuota.Spec = *spec

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to create or update clusterResourceQuota %v: %s", &clusterResourceQuota, err)
	}
	return nil
}

func (r *storageClientReconcile) createOrUpdate(obj client.Object, f controllerutil.MutateFn) error {
	result, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, obj, f)
	if err != nil {
		return err
	}
	r.log.Info(fmt.Sprintf("%s successfully %s", obj.GetObjectKind(), result), "name", obj.GetName())
	return nil
}

func (r *storageClientReconcile) deletionPhase(externalClusterClient *providerClient.OCSProviderClient) (ctrl.Result, error) {
	r.storageClient.Status.Phase = v1alpha1.StorageClientOffboarding
	names, err := r.getClientProfileNames()
	if err != nil {
		return reconcile.Result{}, err
	}

	if len(names) > 0 {
		if exist, err := r.hasPersistentVolumes(names); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to verify persistentvolumes dependent on storageclient %q: %v", r.storageClient.Name, err)
		} else if exist {
			return reconcile.Result{}, fmt.Errorf("one or more persistentvolumes exist that are dependent on storageclient %s", r.storageClient.Name)
		}
		if exist, err := r.hasVolumeSnapshotContents(names); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to verify volumesnapshotcontents dependent on storageclient %q: %v", r.storageClient.Name, err)
		} else if exist {
			return reconcile.Result{}, fmt.Errorf("one or more volumesnapshotcontents exist that are dependent on storageclient %s", r.storageClient.Name)
		}
		if exist, err := r.hasVolumeGroupSnapshotContents(names); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to verify volumegroupsnapshotcontents dependent on storageclient %q: %v", r.storageClient.Name, err)
		} else if exist {
			return reconcile.Result{}, fmt.Errorf("one or more volumegroupsnapshotcontents exist that are dependent on storageclient %s", r.storageClient.Name)
		}
	}

	if res, err := r.offboardConsumer(externalClusterClient); err != nil {
		r.log.Error(err, "Offboarding in progress.")
	} else if !res.IsZero() {
		// result is not empty
		return res, nil
	}
	if controllerutil.RemoveFinalizer(&r.storageClient, storageClientFinalizer) {
		r.log.Info("removing finalizer from StorageClient.", "StorageClient", r.storageClient.Name)
		if err := r.update(&r.storageClient); err != nil {
			r.log.Info("Failed to remove finalizer from StorageClient", "StorageClient", r.storageClient.Name)
			return reconcile.Result{}, fmt.Errorf("failed to remove finalizer from StorageClient: %v", err)
		}
	}
	r.log.Info("StorageClient is offboarded", "StorageClient", r.storageClient.Name)
	return reconcile.Result{}, nil
}

// newExternalClusterClient returns the *providerClient.OCSProviderClient
func (r *storageClientReconcile) newExternalClusterClient() (*providerClient.OCSProviderClient, error) {

	ocsProviderClient, err := providerClient.NewProviderClient(
		r.ctx, r.storageClient.Spec.StorageProviderEndpoint, utils.OcsClientTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create a new provider client with endpoint %v: %v", r.storageClient.Spec.StorageProviderEndpoint, err)
	}

	return ocsProviderClient, nil
}

// onboardConsumer makes an API call to the external storage provider cluster for onboarding
func (r *storageClientReconcile) onboardConsumer(externalClusterClient *providerClient.OCSProviderClient) error {

	// TODO Have a version file corresponding to the release
	csvList := opv1a1.ClusterServiceVersionList{}
	if err := r.list(&csvList, client.InNamespace(r.OperatorNamespace)); err != nil {
		return fmt.Errorf("failed to list csv resources in ns: %v, err: %v", r.OperatorNamespace, err)
	}
	csv := utils.Find(csvList.Items, func(csv *opv1a1.ClusterServiceVersion) bool {
		return strings.HasPrefix(csv.Name, csvPrefix)
	})
	if csv == nil {
		return fmt.Errorf("unable to find csv with prefix %q", csvPrefix)
	}
	onboardRequest := providerClient.NewOnboardConsumerRequest().
		SetOnboardingTicket(r.storageClient.Spec.OnboardingTicket).
		SetClientOperatorVersion(csv.Spec.Version.String())
	response, err := externalClusterClient.OnboardConsumer(r.ctx, onboardRequest)
	if err != nil {
		return fmt.Errorf("failed to onboard consumer: %v", err)
	}

	if response.StorageConsumerUUID == "" {
		err = fmt.Errorf("storage provider response is empty")
		r.log.Error(err, "empty response")
		return err
	}

	r.storageClient.Status.ConsumerID = response.StorageConsumerUUID
	r.storageClient.Status.Phase = v1alpha1.StorageClientConnected

	r.log.Info("onboarding completed")
	return nil
}

// offboardConsumer makes an API call to the external storage provider cluster for offboarding
func (r *storageClientReconcile) offboardConsumer(externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {
	if _, err := externalClusterClient.OffboardConsumer(r.ctx, r.storageClient.Status.ConsumerID); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to offboard consumer: %v", err)
	}
	return reconcile.Result{}, nil
}

func (r *storageClientReconcile) reconcileClientStatusReporterJob() (reconcile.Result, error) {
	cronJob := &batchv1.CronJob{}
	// maximum characters allowed for cronjob name is 52 and below interpolation creates 47 characters
	cronJob.Name = fmt.Sprintf("storageclient-%s-status-reporter", utils.GetMD5Hash(r.storageClient.Name)[:16])
	cronJob.Namespace = r.OperatorNamespace

	var podDeadLineSeconds int64 = 120
	jobDeadLineSeconds := podDeadLineSeconds + 35
	var keepJobResourceSeconds int32 = 600
	var reducedKeptSuccecsful int32 = 1

	_, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, cronJob, func() error {
		if err := r.own(cronJob); err != nil {
			return fmt.Errorf("failed to own cronjob: %v", err)
		}
		// this helps during listing of cronjob by labels corresponding to the storageclient
		utils.AddLabel(cronJob, storageClientNameLabel, r.storageClient.Name)
		cronJob.Spec = batchv1.CronJobSpec{
			Schedule:                   "* * * * *",
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			SuccessfulJobsHistoryLimit: &reducedKeptSuccecsful,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					ActiveDeadlineSeconds:   &jobDeadLineSeconds,
					TTLSecondsAfterFinished: &keepJobResourceSeconds,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ActiveDeadlineSeconds: &podDeadLineSeconds,
							Containers: []corev1.Container{
								{
									Name:  "heartbeat",
									Image: os.Getenv(utils.StatusReporterImageEnvVar),
									Command: []string{
										"/status-reporter",
									},
									Env: []corev1.EnvVar{
										{
											Name:  utils.StorageClientNameEnvVar,
											Value: r.storageClient.Name,
										},
										{
											Name:  utils.OperatorNamespaceEnvVar,
											Value: r.OperatorNamespace,
										},
									},
								},
							},
							RestartPolicy:      corev1.RestartPolicyOnFailure,
							ServiceAccountName: "ocs-client-operator-status-reporter",
							Tolerations: []corev1.Toleration{
								{
									Effect:   corev1.TaintEffectNoSchedule,
									Key:      "node.ocs.openshift.io/storage",
									Operator: corev1.TolerationOpEqual,
									Value:    "true",
								},
							},
						},
					},
				},
			},
		}
		return nil
	})
	if err != nil {
		return reconcile.Result{Requeue: true}, fmt.Errorf("Failed to update cronJob: %v", err)
	}
	return reconcile.Result{}, nil
}

func (r *storageClientReconcile) hasPersistentVolumes(clientProfileNames []string) (bool, error) {
	for _, name := range clientProfileNames {
		pvList := &corev1.PersistentVolumeList{}
		if err := r.list(pvList, client.MatchingFields{pvClusterIDIndexName: name}, client.Limit(1)); err != nil {
			return false, fmt.Errorf("failed to list persistent volumes: %v", err)
		}
		if len(pvList.Items) != 0 {
			r.log.Info(fmt.Sprintf("PersistentVolumes referring storageclient %q exists", r.storageClient.Name))
			return true, nil
		}
	}
	return false, nil
}

func (r *storageClientReconcile) hasVolumeSnapshotContents(clientProfileNames []string) (bool, error) {
	for _, name := range clientProfileNames {
		vscList := &snapapi.VolumeSnapshotContentList{}
		if err := r.list(vscList, client.MatchingFields{vscClusterIDIndexName: name}); err != nil {
			return false, fmt.Errorf("failed to list volume snapshot content resources: %v", err)
		}
		if len(vscList.Items) != 0 {
			r.log.Info(fmt.Sprintf("VolumeSnapshotContent referring storageclient %q exists", r.storageClient.Name))
			return true, nil
		}
	}
	return false, nil
}

func (r *storageClientReconcile) hasVolumeGroupSnapshotContents(clientProfileNames []string) (bool, error) {
	if val, _ := r.crdsBeingWatched.Load(VolumeGroupSnapshotClassCrdName); !val.(bool) {
		return false, nil
	}
	for _, name := range clientProfileNames {
		vscList := &groupsnapapi.VolumeGroupSnapshotContentList{}
		if err := r.list(vscList, client.MatchingFields{vgscClusterIDIndexName: name}); err != nil {
			return false, fmt.Errorf("failed to list volume group snapshot content resources: %v", err)
		}
		if len(vscList.Items) != 0 {
			r.log.Info(fmt.Sprintf("VolumeGroupSnapshotContent referring storageclient %q exists", r.storageClient.Name))
			return true, nil
		}
	}

	return false, nil
}

func (r *storageClientReconcile) getClientProfileNames() ([]string, error) {
	clientProfileList := &csiopv1a1.ClientProfileList{}
	if err := r.list(clientProfileList, client.MatchingFields{ownerUIDIndexName: string(r.storageClient.UID)}); err != nil {
		return nil, fmt.Errorf("failed to list clientprofiles owned by storageclient %s: %v", r.storageClient.Name, err)
	}
	clientProfileNames := make([]string, 0, len(clientProfileList.Items))
	for idx := range clientProfileList.Items {
		clientProfileNames = append(clientProfileNames, clientProfileList.Items[idx].Name)
	}
	return clientProfileNames, nil
}

func (r *storageClientReconcile) list(obj client.ObjectList, listOptions ...client.ListOption) error {
	return r.Client.List(r.ctx, obj, listOptions...)
}

func (r *storageClientReconcile) get(obj client.Object, opts ...client.GetOption) error {
	key := client.ObjectKeyFromObject(obj)
	return r.Get(r.ctx, key, obj, opts...)
}

func (r *storageClientReconcile) update(obj client.Object, opts ...client.UpdateOption) error {
	return r.Update(r.ctx, obj, opts...)
}

func (r *storageClientReconcile) own(dependent metav1.Object) error {
	return controllerutil.SetOwnerReference(&r.storageClient, dependent, r.Scheme)
}

func removeStorageClaimAsOwner(obj client.Object) {
	refs := obj.GetOwnerReferences()
	if idx := slices.IndexFunc(refs, func(owner metav1.OwnerReference) bool {
		return owner.Kind == "StorageClaim"
	}); idx != -1 {
		obj.SetOwnerReferences(slices.Delete(refs, idx, idx+1))
	}
}

func (r *storageClientReconcile) EnsureClusterScopedResource(obj client.Object, extRes *providerpb.ExternalResource, useReplace bool) error {
	obj.SetName(extRes.Name)

	mutateFunc := func() error {
		if err := json.Unmarshal(extRes.Data, obj); err != nil {
			return fmt.Errorf("failed to unmarshal %s configuration response: %v", obj.GetName(), err)
		}
		removeStorageClaimAsOwner(obj)
		if err := r.own(obj); err != nil {
			return fmt.Errorf("failed to own %s resource: %v", obj.GetName(), err)
		}
		utils.AddLabels(obj, extRes.Labels)
		utils.AddAnnotations(obj, extRes.Annotations)
		return nil
	}

	var err error
	if useReplace {
		err = utils.CreateOrReplace(r.ctx, r.Client, obj, mutateFunc)
	} else {
		err = r.createOrUpdate(obj, mutateFunc)
	}

	if err != nil {
		return fmt.Errorf("failed to create or update: %v", err)
	}
	return nil
}
