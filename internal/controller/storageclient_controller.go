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

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	replicationv1a1 "github.com/csi-addons/kubernetes-csi-addons/api/replication.storage/v1alpha1"
	snapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	nbv1 "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	configv1 "github.com/openshift/api/config/v1"
	quotav1 "github.com/openshift/api/quota/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// grpcCallNames
	OnboardConsumer       = "OnboardConsumer"
	OffboardConsumer      = "OffboardConsumer"
	GetStorageConfig      = "GetStorageConfig"
	AcknowledgeOnboarding = "AcknowledgeOnboarding"

	storageClientNameLabel = "ocs.openshift.io/storageclient.name"
	storageClientFinalizer = "storageclient.ocs.openshift.io"

	// indexes for caching
	pvClusterIDIndexName  = "index:persistentVolumeClusterID"
	vscClusterIDIndexName = "index:volumeSnapshotContentCSIDriver"

	csvPrefix = "ocs-client-operator"
)

// StorageClientReconciler reconciles a StorageClient object
type StorageClientReconciler struct {
	client.Client

	Scheme            *runtime.Scheme
	OperatorNamespace string

	log               klog.Logger
	ctx               context.Context
	recorder          *utils.EventReporter
	storageClient     *v1alpha1.StorageClient
	storageClientHash string
}

// SetupWithManager sets up the controller with the Manager.
func (r *StorageClientReconciler) SetupWithManager(mgr ctrl.Manager) error {
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

	r.recorder = utils.NewEventReporter(mgr.GetEventRecorderFor("controller_storageclient"))
	generationChangePredicate := predicate.GenerationChangedPredicate{}
	bldr := ctrl.NewControllerManagedBy(mgr).
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
		Owns(&csiopv1a1.ClientProfile{}, builder.WithPredicates(generationChangePredicate))

	return bldr.Complete(r)
}

//+kubebuilder:rbac:groups=quota.openshift.io,resources=clusterresourcequotas,verbs=get;list;watch;create;update
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients/finalizers,verbs=update
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch
//+kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;create;update;watch;delete
//+kubebuilder:rbac:groups=operators.coreos.com,resources=clusterserviceversions,verbs=get;list;watch
//+kubebuilder:rbac:groups=csi.ceph.io,resources=cephconnections,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=noobaa.io,resources=noobaas,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=csi.ceph.io,resources=clientprofilemappings,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotclasses,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotcontents,verbs=get;list;watch
//+kubebuilder:rbac:groups=csi.ceph.io,resources=clientprofiles,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=replication.storage.openshift.io,resources=volumereplicationclasses,verbs=get;list;watch;create;delete

func (r *StorageClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error
	r.ctx = ctx
	r.log = log.FromContext(ctx, "StorageClient", req)
	r.log.Info("Reconciling StorageClient")

	r.storageClient = &v1alpha1.StorageClient{}
	r.storageClient.Name = req.Name
	if err = r.get(r.storageClient); err != nil {
		if kerrors.IsNotFound(err) {
			r.log.Info("StorageClient resource not found. Ignoring since object must be deleted.")
			return reconcile.Result{}, nil
		}
		r.log.Error(err, "Failed to get StorageClient.")
		return reconcile.Result{}, fmt.Errorf("failed to get StorageClient: %v", err)
	}

	// Dont Reconcile the StorageClient if it is in failed state
	if r.storageClient.Status.Phase == v1alpha1.StorageClientFailed {
		return reconcile.Result{}, nil
	}

	r.storageClientHash = utils.GetMD5Hash(r.storageClient.Name)
	result, reconcileErr := r.reconcilePhases()

	// Apply status changes to the StorageClient
	statusErr := r.Client.Status().Update(ctx, r.storageClient)
	if statusErr != nil {
		r.log.Error(statusErr, "Failed to update StorageClient status.")
	}
	if reconcileErr != nil {
		err = reconcileErr
	} else if statusErr != nil {
		err = statusErr
	}
	return result, err
}

func (r *StorageClientReconciler) reconcilePhases() (ctrl.Result, error) {

	externalClusterClient, err := r.newExternalClusterClient()
	if err != nil {
		return reconcile.Result{}, err
	}
	defer externalClusterClient.Close()

	// deletion phase
	if !r.storageClient.GetDeletionTimestamp().IsZero() {
		return r.deletionPhase(externalClusterClient)
	}

	storageClients := &v1alpha1.StorageClientList{}
	if err := r.list(storageClients); err != nil {
		r.log.Error(err, "unable to list storage clients")
		return ctrl.Result{}, err
	}

	// ensure finalizer
	if controllerutil.AddFinalizer(r.storageClient, storageClientFinalizer) {
		r.storageClient.Status.Phase = v1alpha1.StorageClientInitializing
		r.log.Info("Finalizer not found for StorageClient. Adding finalizer.", "StorageClient", r.storageClient.Name)
		if err := r.update(r.storageClient); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update StorageClient: %v", err)
		}
	}

	if r.storageClient.Status.ConsumerID == "" {
		return r.onboardConsumer(externalClusterClient)
	} else if r.storageClient.Status.Phase == v1alpha1.StorageClientOnboarding {
		return r.acknowledgeOnboarding(externalClusterClient)
	}

	storageClientResponse, err := externalClusterClient.GetStorageConfig(r.ctx, r.storageClient.Status.ConsumerID)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get StorageConfig: %v", err)
	}

	if storageClientResponse.SystemAttributes != nil {
		r.storageClient.Status.InMaintenanceMode = storageClientResponse.SystemAttributes.SystemInMaintenanceMode
	}

	if res, err := r.reconcileClientStatusReporterJob(); err != nil {
		return res, err
	}

	for _, eResource := range storageClientResponse.ExternalResource {
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
			cephConnection.Name = r.storageClient.Name
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
				return nil
			}); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to reconcile clientProfileMapping: %v", err)
			}
		case "Secret":
			data := map[string]string{}
			if err := json.Unmarshal(eResource.Data, &data); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to unmarshall secret: %v", err)
			}
			secret := &corev1.Secret{}
			secret.Name = eResource.Name
			secret.Namespace = r.OperatorNamespace
			_, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, secret, func() error {
				if err := r.own(secret); err != nil {
					return err
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
			data := map[string]string{}
			err = json.Unmarshal(eResource.Data, &data)
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to unmarshal storage configuration response: %v", err)
			}
			var storageClass *storagev1.StorageClass
			data["csi.storage.k8s.io/provisioner-secret-namespace"] = r.OperatorNamespace
			data["csi.storage.k8s.io/node-stage-secret-namespace"] = r.OperatorNamespace
			data["csi.storage.k8s.io/controller-expand-secret-namespace"] = r.OperatorNamespace

			if eResource.Name == "cephfs" {
				storageClass = r.getCephFSStorageClass()
			} else if eResource.Name == "ceph-rbd" {
				storageClass = r.getCephRBDStorageClass()
			}
			data["clusterID"] = r.storageClientHash
			err = utils.CreateOrReplace(r.ctx, r.Client, storageClass, func() error {
				if err := r.own(storageClass); err != nil {
					return fmt.Errorf("failed to own Storage Class resource: %v", err)
				}
				utils.AddLabels(storageClass, eResource.Labels)
				storageClass.Parameters = data
				return nil
			})
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to create or update StorageClass: %s", err)
			}
		case "VolumeSnapshotClass":
			data := map[string]string{}
			err = json.Unmarshal(eResource.Data, &data)
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to unmarshal storage configuration response: %v", err)
			}
			var volumeSnapshotClass *snapapi.VolumeSnapshotClass
			data["csi.storage.k8s.io/snapshotter-secret-namespace"] = r.OperatorNamespace
			if eResource.Name == "cephfs" {
				volumeSnapshotClass = r.getCephFSVolumeSnapshotClass()
			} else if eResource.Name == "ceph-rbd" {
				volumeSnapshotClass = r.getCephRBDVolumeSnapshotClass()
			}
			data["clusterID"] = r.storageClientHash
			err = utils.CreateOrReplace(r.ctx, r.Client, volumeSnapshotClass, func() error {
				if err := r.own(volumeSnapshotClass); err != nil {
					return fmt.Errorf("failed to own VolumeSnapshotClass resource: %v", err)
				}
				utils.AddLabels(volumeSnapshotClass, eResource.Labels)
				volumeSnapshotClass.Parameters = data
				return nil
			})
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to create or update VolumeSnapshotClass: %s", err)
			}
		case "VolumeReplicationClass":
			vrc := &replicationv1a1.VolumeReplicationClass{}
			vrc.Name = eResource.Name
			err := utils.CreateOrReplace(r.ctx, r.Client, vrc, func() error {
				if err := r.own(vrc); err != nil {
					return fmt.Errorf("failed to own VolumeReplicationClass resource: %v", err)
				}
				if err := json.Unmarshal(eResource.Data, &vrc.Spec); err != nil {
					return fmt.Errorf("failed to unmarshall VolumeReplicationClass spec: %v", err)
				}
				vrc.Spec.Parameters["replication.storage.openshift.io/replication-secret-namespace"] = r.OperatorNamespace

				utils.AddLabels(vrc, eResource.Labels)
				utils.AddAnnotations(vrc, eResource.Annotations)

				return nil
			})
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to create or update VolumeReplicationClass: %s", err)
			}
		case "ClientProfile":
			clientProfile := &csiopv1a1.ClientProfile{}
			clientProfile.Name = r.storageClientHash
			clientProfile.Namespace = r.OperatorNamespace
			if _, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, clientProfile, func() error {
				if err := r.own(clientProfile); err != nil {
					return fmt.Errorf("failed to own clientProfile resource: %v", err)
				}
				if err := json.Unmarshal(eResource.Data, &clientProfile.Spec); err != nil {
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

	if utils.AddAnnotation(r.storageClient, utils.DesiredConfigHashAnnotationKey, storageClientResponse.DesiredConfigHash) {
		if err := r.update(r.storageClient); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update StorageClient with desired config hash annotation: %v", err)
		}
	}

	return reconcile.Result{}, nil
}

func (r *StorageClientReconciler) reconcileClusterResourceQuota(spec *quotav1.ClusterResourceQuotaSpec) error {
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

func (r *StorageClientReconciler) createOrUpdate(obj client.Object, f controllerutil.MutateFn) error {
	result, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, obj, f)
	if err != nil {
		return err
	}
	r.log.Info(fmt.Sprintf("%s successfully %s", obj.GetObjectKind(), result), "name", obj.GetName())
	return nil
}

func (r *StorageClientReconciler) deletionPhase(externalClusterClient *providerClient.OCSProviderClient) (ctrl.Result, error) {
	r.storageClient.Status.Phase = v1alpha1.StorageClientOffboarding

	if exist, err := r.hasPersistentVolumes(); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to verify persistentvolumes dependent on storageclient %q: %v", r.storageClient.Name, err)
	} else if exist {
		return reconcile.Result{}, fmt.Errorf("one or more persistentvolumes exist that are dependent on storageclient %s", r.storageClient.Name)
	}
	if exist, err := r.hasVolumeSnapshotContents(); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to verify volumesnapshotcontents dependent on storageclient %q: %v", r.storageClient.Name, err)
	} else if exist {
		return reconcile.Result{}, fmt.Errorf("one or more volumesnapshotcontents exist that are dependent on storageclient %s", r.storageClient.Name)
	}

	if res, err := r.offboardConsumer(externalClusterClient); err != nil {
		r.log.Error(err, "Offboarding in progress.")
	} else if !res.IsZero() {
		// result is not empty
		return res, nil
	}

	if controllerutil.RemoveFinalizer(r.storageClient, storageClientFinalizer) {
		r.log.Info("removing finalizer from StorageClient.", "StorageClient", r.storageClient.Name)
		if err := r.update(r.storageClient); err != nil {
			r.log.Info("Failed to remove finalizer from StorageClient", "StorageClient", r.storageClient.Name)
			return reconcile.Result{}, fmt.Errorf("failed to remove finalizer from StorageClient: %v", err)
		}
	}
	r.log.Info("StorageClient is offboarded", "StorageClient", r.storageClient.Name)
	return reconcile.Result{}, nil
}

// newExternalClusterClient returns the *providerClient.OCSProviderClient
func (r *StorageClientReconciler) newExternalClusterClient() (*providerClient.OCSProviderClient, error) {

	ocsProviderClient, err := providerClient.NewProviderClient(
		r.ctx, r.storageClient.Spec.StorageProviderEndpoint, utils.OcsClientTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create a new provider client with endpoint %v: %v", r.storageClient.Spec.StorageProviderEndpoint, err)
	}

	return ocsProviderClient, nil
}

// onboardConsumer makes an API call to the external storage provider cluster for onboarding
func (r *StorageClientReconciler) onboardConsumer(externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	// TODO Need to find a way to get rid of ClusterVersion here as it is OCP
	// specific one.
	clusterVersion := &configv1.ClusterVersion{}
	clusterVersion.Name = "version"
	if err := r.get(clusterVersion); err != nil {
		r.log.Error(err, "failed to get the clusterVersion version of the OCP cluster")
		return reconcile.Result{}, fmt.Errorf("failed to get the clusterVersion version of the OCP cluster: %v", err)
	}

	// TODO Have a version file corresponding to the release
	csvList := opv1a1.ClusterServiceVersionList{}
	if err := r.list(&csvList, client.InNamespace(r.OperatorNamespace)); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list csv resources in ns: %v, err: %v", r.OperatorNamespace, err)
	}
	csv := utils.Find(csvList.Items, func(csv *opv1a1.ClusterServiceVersion) bool {
		return strings.HasPrefix(csv.Name, csvPrefix)
	})
	if csv == nil {
		return reconcile.Result{}, fmt.Errorf("unable to find csv with prefix %q", csvPrefix)
	}
	name := fmt.Sprintf("storageconsumer-%s", clusterVersion.Spec.ClusterID)
	onboardRequest := providerClient.NewOnboardConsumerRequest().
		SetConsumerName(name).
		SetOnboardingTicket(r.storageClient.Spec.OnboardingTicket).
		SetClientOperatorVersion(csv.Spec.Version.String())
	response, err := externalClusterClient.OnboardConsumer(r.ctx, onboardRequest)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			r.logGrpcErrorAndReportEvent(OnboardConsumer, err, st.Code())
		}
		return reconcile.Result{}, fmt.Errorf("failed to onboard consumer: %v", err)
	}

	if response.StorageConsumerUUID == "" {
		err = fmt.Errorf("storage provider response is empty")
		r.log.Error(err, "empty response")
		return reconcile.Result{}, err
	}

	r.storageClient.Status.ConsumerID = response.StorageConsumerUUID
	r.storageClient.Status.Phase = v1alpha1.StorageClientOnboarding

	r.log.Info("onboarding started")
	return reconcile.Result{Requeue: true}, nil
}

func (r *StorageClientReconciler) acknowledgeOnboarding(externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	_, err := externalClusterClient.AcknowledgeOnboarding(r.ctx, r.storageClient.Status.ConsumerID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			r.logGrpcErrorAndReportEvent(AcknowledgeOnboarding, err, st.Code())
		}
		r.log.Error(err, "Failed to acknowledge onboarding.")
		return reconcile.Result{}, fmt.Errorf("failed to acknowledge onboarding: %v", err)
	}
	r.storageClient.Status.Phase = v1alpha1.StorageClientConnected

	r.log.Info("Onboarding is acknowledged successfully.")
	return reconcile.Result{Requeue: true}, nil
}

// offboardConsumer makes an API call to the external storage provider cluster for offboarding
func (r *StorageClientReconciler) offboardConsumer(externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	_, err := externalClusterClient.OffboardConsumer(r.ctx, r.storageClient.Status.ConsumerID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			r.logGrpcErrorAndReportEvent(OffboardConsumer, err, st.Code())
		}
		return reconcile.Result{}, fmt.Errorf("failed to offboard consumer: %v", err)
	}

	return reconcile.Result{}, nil
}

func (r *StorageClientReconciler) logGrpcErrorAndReportEvent(grpcCallName string, err error, errCode codes.Code) {

	var msg, eventReason, eventType string

	if grpcCallName == OnboardConsumer {
		if errCode == codes.InvalidArgument {
			msg = "Token is invalid. Verify the token again or contact the provider admin"
			eventReason = "TokenInvalid"
			eventType = corev1.EventTypeWarning
		} else if errCode == codes.AlreadyExists {
			msg = "Token is already used. Contact provider admin for a new token"
			eventReason = "TokenAlreadyUsed"
			eventType = corev1.EventTypeWarning
		}
	} else if grpcCallName == AcknowledgeOnboarding {
		if errCode == codes.NotFound {
			msg = "StorageConsumer not found. Contact the provider admin"
			eventReason = "NotFound"
			eventType = corev1.EventTypeWarning
		}
	} else if grpcCallName == OffboardConsumer {
		if errCode == codes.InvalidArgument {
			msg = "StorageConsumer UID is not valid. Contact the provider admin"
			eventReason = "UIDInvalid"
			eventType = corev1.EventTypeWarning
		}
	} else if grpcCallName == GetStorageConfig {
		if errCode == codes.InvalidArgument {
			msg = "StorageConsumer UID is not valid. Contact the provider admin"
			eventReason = "UIDInvalid"
			eventType = corev1.EventTypeWarning
		} else if errCode == codes.NotFound {
			msg = "StorageConsumer UID not found. Contact the provider admin"
			eventReason = "UIDNotFound"
			eventType = corev1.EventTypeWarning
		} else if errCode == codes.Unavailable {
			msg = "StorageConsumer is not ready yet. Will requeue after 5 second"
			eventReason = "NotReady"
			eventType = corev1.EventTypeNormal
		}
	}

	if msg != "" {
		r.log.Error(err, "StorageProvider:"+grpcCallName+":"+msg)
		r.recorder.ReportIfNotPresent(r.storageClient, eventType, eventReason, msg)
	}
}

func (r *StorageClientReconciler) reconcileClientStatusReporterJob() (reconcile.Result, error) {
	cronJob := &batchv1.CronJob{}
	// maximum characters allowed for cronjob name is 52 and below interpolation creates 47 characters
	cronJob.Name = fmt.Sprintf("storageclient-%s-status-reporter", r.storageClientHash[:16])
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

func (r *StorageClientReconciler) hasPersistentVolumes() (bool, error) {
	pvList := &corev1.PersistentVolumeList{}
	if err := r.list(pvList, client.MatchingFields{pvClusterIDIndexName: r.storageClientHash}, client.Limit(1)); err != nil {
		return false, fmt.Errorf("failed to list persistent volumes: %v", err)
	}

	if len(pvList.Items) != 0 {
		r.log.Info(fmt.Sprintf("PersistentVolumes referring storageclient %q exists", r.storageClient.Name))
		return true, nil
	}

	return false, nil
}

func (r *StorageClientReconciler) hasVolumeSnapshotContents() (bool, error) {
	vscList := &snapapi.VolumeSnapshotContentList{}
	if err := r.list(vscList, client.MatchingFields{vscClusterIDIndexName: r.storageClientHash}); err != nil {
		return false, fmt.Errorf("failed to list volume snapshot content resources: %v", err)
	}

	if len(vscList.Items) != 0 {
		r.log.Info(fmt.Sprintf("VolumeSnapshotContent referring storageclient %q exists", r.storageClient.Name))
		return true, nil
	}

	return false, nil
}

func (r *StorageClientReconciler) getCephFSStorageClass() *storagev1.StorageClass {
	pvReclaimPolicy := corev1.PersistentVolumeReclaimDelete
	allowVolumeExpansion := true
	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-cephfs", r.storageClient.Name),
			Annotations: map[string]string{
				"description": "Provides RWO and RWX Filesystem volumes",
			},
		},
		ReclaimPolicy:        &pvReclaimPolicy,
		AllowVolumeExpansion: &allowVolumeExpansion,
		Provisioner:          templates.CephFsDriverName,
	}
	return storageClass
}

func (r *StorageClientReconciler) getCephRBDStorageClass() *storagev1.StorageClass {
	pvReclaimPolicy := corev1.PersistentVolumeReclaimDelete
	allowVolumeExpansion := true
	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-rbd", r.storageClient.Name),
			Annotations: map[string]string{
				"description": "Provides RWO Filesystem volumes, and RWO and RWX Block volumes",
				"reclaimspace.csiaddons.openshift.io/schedule": "@weekly",
			},
		},
		ReclaimPolicy:        &pvReclaimPolicy,
		AllowVolumeExpansion: &allowVolumeExpansion,
		Provisioner:          templates.RBDDriverName,
	}

	// TODO: storageclass should contain keyrotation annotation while sending from provider
	return storageClass
}

func (r *StorageClientReconciler) getCephFSVolumeSnapshotClass() *snapapi.VolumeSnapshotClass {
	volumesnapshotclass := &snapapi.VolumeSnapshotClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-cephfs", r.storageClient.Name),
		},
		Driver:         templates.CephFsDriverName,
		DeletionPolicy: snapapi.VolumeSnapshotContentDelete,
	}
	return volumesnapshotclass
}

func (r *StorageClientReconciler) getCephRBDVolumeSnapshotClass() *snapapi.VolumeSnapshotClass {
	volumesnapshotclass := &snapapi.VolumeSnapshotClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-rbd", r.storageClient.Name),
		},
		Driver:         templates.RBDDriverName,
		DeletionPolicy: snapapi.VolumeSnapshotContentDelete,
	}
	return volumesnapshotclass
}

func (r *StorageClientReconciler) list(obj client.ObjectList, listOptions ...client.ListOption) error {
	return r.Client.List(r.ctx, obj, listOptions...)
}

func (r *StorageClientReconciler) get(obj client.Object, opts ...client.GetOption) error {
	key := client.ObjectKeyFromObject(obj)
	return r.Get(r.ctx, key, obj, opts...)
}

func (r *StorageClientReconciler) update(obj client.Object, opts ...client.UpdateOption) error {
	return r.Update(r.ctx, obj, opts...)
}

func (r *StorageClientReconciler) own(dependent metav1.Object) error {
	return controllerutil.SetOwnerReference(r.storageClient, dependent, r.Scheme)
}
