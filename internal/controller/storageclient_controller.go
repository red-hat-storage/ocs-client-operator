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
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	"go.uber.org/multierr"

	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	replicationv1a1 "github.com/csi-addons/kubernetes-csi-addons/api/replication.storage/v1alpha1"
	"github.com/go-logr/logr"
	groupsnapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumegroupsnapshot/v1beta1"
	snapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	nbv1 "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	quotav1 "github.com/openshift/api/quota/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
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

	VolumeGroupSnapshotClassCrdName = "volumegroupsnapshotclasses.groupsnapshot.storage.k8s.io"
)

var (
	csiDrivers = []string{
		templates.RBDDriverName,
		templates.CephFsDriverName,
	}
	kindsToReconcile = []client.Object{
		&quotav1.ClusterResourceQuota{},
		&csiopv1a1.CephConnection{},
		&csiopv1a1.ClientProfileMapping{},
		&corev1.Secret{},
		&nbv1.NooBaa{},
		&storagev1.StorageClass{},
		&snapapi.VolumeSnapshotClass{},
		&replicationv1a1.VolumeReplicationClass{},
		&csiopv1a1.ClientProfile{},
		&groupsnapapi.VolumeGroupSnapshotClass{},
	}
)

type desiredKubeObject struct {
	types.NamespacedName
	bytes []byte
}

type desiredKubeObjects []desiredKubeObject

// StorageClientReconciler reconciles a StorageClient object
type StorageClientReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	OperatorNamespace string
	OperatorPodName   string

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
//+kubebuilder:rbac:groups=replication.storage.openshift.io,resources=volumereplicationclasses,verbs=get;list;watch;create;delete;update
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;delete;update
//+kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotclasses,verbs=get;list;watch;create;delete;update
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotcontents,verbs=get;list;watch
//+kubebuilder:rbac:groups=groupsnapshot.storage.k8s.io,resources=volumegroupsnapshotclasses,verbs=get;list;watch;create;delete;update
//+kubebuilder:rbac:groups=groupsnapshot.storage.k8s.io,resources=volumegroupsnapshotcontents,verbs=get;list;watch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclaims,verbs=get;list;watch;delete;patch

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

	operatorVersion, err := r.getOperatorVersion()
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get operator version: %v", err)
	}

	if r.storageClient.Status.ConsumerID == "" {
		if err := r.onboardConsumer(externalClusterClient, operatorVersion); err != nil {
			return reconcile.Result{}, err
		}
	}

	if res, err := r.reconcileClientStatusReporterJob(operatorVersion); err != nil {
		return res, err
	}

	storageClientResponse, err := externalClusterClient.GetDesiredClientState(r.ctx, r.storageClient.Status.ConsumerID)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get StorageConfig: %v", err)
	}

	r.storageClient.Status.InMaintenanceMode = storageClientResponse.MaintenanceMode

	kubeObjectsByGvk := map[string]desiredKubeObjects{}
	for _, kubeObj := range storageClientResponse.KubeObjects {
		if kubeObj == nil {
			continue
		}
		objectMeta := &metav1.PartialObjectMetadata{}
		if err := json.Unmarshal(kubeObj.Bytes, objectMeta); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to unmarshal metadata for the Object: %w", err)
		}
		gvk := objectMeta.GroupVersionKind().String()
		kubeObjectsByGvk[gvk] = append(
			kubeObjectsByGvk[gvk],
			desiredKubeObject{
				NamespacedName: client.ObjectKeyFromObject(objectMeta),
				bytes:          kubeObj.Bytes,
			},
		)
	}
	var combinedErr error
	for _, kind := range kindsToReconcile {
		r.reconcileResourcesByGVK(kind, kubeObjectsByGvk, combinedErr)
	}
	if combinedErr != nil {
		return reconcile.Result{}, combinedErr
	}

	update := false
	if storageClientResponse.ClientOperatorChannel != "" {
		if utils.AddAnnotation(&r.storageClient, utils.DesiredSubscriptionChannelAnnotationKey, storageClientResponse.ClientOperatorChannel) {
			update = true
		}
	}
	if utils.AddAnnotation(&r.storageClient, utils.DesiredConfigHashAnnotationKey, storageClientResponse.DesiredStateHash) {
		update = true
	}
	if update {
		if err := r.update(&r.storageClient); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update StorageClient with desired config hash annotation: %v", err)
		}
	}

	// Deleting pre 4.19 built-in storage claims
	if err = r.deleteStorageClaims(fmt.Sprintf("%s-ceph-rbd", r.storageClient.Name)); err != nil {
		return reconcile.Result{}, err
	}
	if err = r.deleteStorageClaims(fmt.Sprintf("%s-cephfs", r.storageClient.Name)); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *storageClientReconcile) deleteStorageClaims(claimName string) error {
	storageClaim := &metav1.PartialObjectMetadata{}
	storageClaim.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("StorageClaim"))
	storageClaim.Name = claimName

	if err := r.get(storageClaim); client.IgnoreNotFound(err) != nil && !meta.IsNoMatchError(err) {
		return fmt.Errorf("Failed to get StorageClaim %q: %w", claimName, err)
	}
	if storageClaim.UID != "" {
		if err := r.Client.Delete(r.ctx, storageClaim); err != nil {
			return fmt.Errorf("Failed to delete StorageClaim %q: %v", claimName, err)
		}

		original := storageClaim.DeepCopy()
		if controllerutil.RemoveFinalizer(storageClaim, "storageclaim.ocs.openshift.io") {
			if err := r.Client.Patch(r.ctx, storageClaim, client.MergeFrom(original)); err != nil {
				return fmt.Errorf("Failed to patch StorageClaim %q: %v", claimName, err)
			}
			r.log.Info("StorageClaim finalizer removed", "name", claimName)
		}
	}
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

	if err := r.offboardConsumer(externalClusterClient); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to offboard consumer for storageclient %v: %v", r.storageClient.Name, err)
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
func (r *storageClientReconcile) onboardConsumer(externalClusterClient *providerClient.OCSProviderClient, operatorVersion string) error {
	onboardRequest := providerClient.NewOnboardConsumerRequest().
		SetOnboardingTicket(r.storageClient.Spec.OnboardingTicket).
		SetClientOperatorVersion(operatorVersion)
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
func (r *storageClientReconcile) offboardConsumer(externalClusterClient *providerClient.OCSProviderClient) error {
	if _, err := externalClusterClient.OffboardConsumer(r.ctx, r.storageClient.Status.ConsumerID); err != nil {
		return fmt.Errorf("failed to offboard consumer: %v", err)
	}
	return nil
}

func (r *storageClientReconcile) reconcileClientStatusReporterJob(operatorVersion string) (reconcile.Result, error) {
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
										{
											Name:  utils.OperatorVersionEnvVar,
											Value: operatorVersion,
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
	return controllerutil.SetControllerReference(&r.storageClient, dependent, r.Scheme)
}

func removeStorageClaimAsOwner(obj client.Object) {
	refs := obj.GetOwnerReferences()
	if idx := slices.IndexFunc(refs, func(owner metav1.OwnerReference) bool {
		return owner.Kind == "StorageClaim"
	}); idx != -1 {
		obj.SetOwnerReferences(slices.Delete(refs, idx, idx+1))
	}
}

func (r *storageClientReconcile) reconcileResourcesByGVK(
	kind client.Object,
	desiredObjects map[string]desiredKubeObjects,
	combinedErr error,
) {
	gvk, err := apiutil.GVKForObject(kind, r.Scheme)
	if err != nil {
		r.log.Error(err, "failed to get gvk")
		multierr.AppendInto(&combinedErr, err)
		return
	}

	objectsToReconcile := desiredObjects[gvk.String()]
	reconciledObjects := make(map[types.NamespacedName]bool, len(objectsToReconcile))
	for idx := range objectsToReconcile {
		// object supplied to reconcile mutates it, we either need to send
		// fresh copy or zero out fields on the mutated object, doing the former
		// as we don't know the fields that needs to be zeroed for every concrete object
		goType := reflect.TypeOf(kind).Elem()
		untypedInstance := reflect.New(goType).Interface()
		kubeObject := untypedInstance.(client.Object)

		desiredState := objectsToReconcile[idx]
		if err := r.reconcileResource(kubeObject, desiredState); err != nil {
			multierr.AppendInto(&combinedErr, err)
		} else {
			reconciledObjects[desiredState.NamespacedName] = true
		}
	}

	existingObjList := &metav1.PartialObjectMetadataList{}
	existingObjList.SetGroupVersionKind(gvk)
	if err := r.list(existingObjList); err != nil && !meta.IsNoMatchError(err) {
		multierr.AppendInto(&combinedErr, err)
		r.log.Error(err, "failed to list resources")
	}
	for idx := range existingObjList.Items {
		obj := &existingObjList.Items[idx]
		if !reconciledObjects[client.ObjectKeyFromObject(obj)] && metav1.IsControlledBy(obj, &r.storageClient) {
			if err := r.Delete(r.ctx, obj); client.IgnoreNotFound(err) != nil {
				multierr.AppendInto(&combinedErr, err)
				r.log.Error(err, "failed to delete object", "Name", client.ObjectKeyFromObject(obj))
			}
		}
	}
}

func (r *storageClientReconcile) reconcileResource(obj client.Object, desiredState desiredKubeObject) error {

	mutateFunc := func() error {
		// Unmarshal follows merge semantics, that means that we don't need to worry about overriding the status,
		// or any metadata fields. There is an exception when it comes to creationTimestamp which gets serialized into
		// default value.
		creationTimestamp := obj.GetCreationTimestamp()
		if err := json.Unmarshal(desiredState.bytes, obj); err != nil {
			return fmt.Errorf("failed to unmarshal %s configuration response: %v", obj.GetName(), err)
		}
		obj.SetCreationTimestamp(creationTimestamp)
		removeStorageClaimAsOwner(obj)
		if err := r.own(obj); err != nil {
			return fmt.Errorf("failed to own %s resource: %v", obj.GetName(), err)
		}
		return nil
	}

	var err error
	obj.SetName(desiredState.Name)
	obj.SetNamespace(desiredState.Namespace)
	_, err = controllerutil.CreateOrUpdate(r.ctx, r.Client, obj, mutateFunc)
	if utils.IsForbiddenError(err) {
		if err := r.Client.Delete(r.ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf(
				"failed to replace %v %v/%v: %v",
				obj.GetObjectKind().GroupVersionKind(),
				obj.GetNamespace(),
				obj.GetName(),
				err,
			)
		}

		// k8s doesn't allow us to create objects when resourceVersion is set, as we are DeepCopying the
		// object, the resource version also gets copied, hence we need to set it to empty before creating it
		obj.SetResourceVersion("")
		if err := r.Client.Create(r.ctx, obj); err != nil {
			return fmt.Errorf(
				"failed to replace %v %v/%v: %v",
				obj.GetObjectKind().GroupVersionKind(),
				obj.GetNamespace(),
				obj.GetName(),
				err,
			)
		}
	} else if meta.IsNoMatchError(err) {
		r.log.Info(
			"Skipping! object type does not exist",
			"name",
			client.ObjectKeyFromObject(obj),
		)
	} else if err != nil {
		return fmt.Errorf(
			"failed to create or update %v %v/%v: %v",
			obj.GetObjectKind().GroupVersionKind(),
			obj.GetNamespace(),
			obj.GetName(),
			err,
		)
	}

	return nil
}

func (r *storageClientReconcile) getOperatorVersion() (string, error) {
	deploymentName := r.OperatorPodName
	for range 2 {
		if i := strings.LastIndex(deploymentName, "-"); i > -1 {
			deploymentName = deploymentName[:i]
		} else {
			return "", fmt.Errorf("failed to derive deployment name from pod name")
		}
	}

	deployment := &metav1.PartialObjectMetadata{}
	deployment.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
	deployment.Name = deploymentName
	deployment.Namespace = r.OperatorNamespace
	if err := r.get(deployment); err != nil {
		return "", fmt.Errorf("failed to get deployment: %v", err)
	}
	ownerCsvIdx := slices.IndexFunc(deployment.OwnerReferences, func(owner metav1.OwnerReference) bool {
		return owner.Kind == "ClusterServiceVersion"
	})
	if ownerCsvIdx == -1 {
		return "", fmt.Errorf("unable to find csv from deployment owners")
	}

	csv := &opv1a1.ClusterServiceVersion{}
	csv.Name = deployment.OwnerReferences[ownerCsvIdx].Name
	csv.Namespace = r.OperatorNamespace
	if err := r.get(csv); err != nil {
		return "", fmt.Errorf("failed to get csv: %v", err)
	}
	return csv.Spec.Version.String(), nil
}
