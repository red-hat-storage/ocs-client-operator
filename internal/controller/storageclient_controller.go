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
	"strings"

	quotav1 "github.com/openshift/api/quota/v1"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	nbv1 "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	configv1 "github.com/openshift/api/config/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
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

	storageClientNameLabel             = "ocs.openshift.io/storageclient.name"
	storageClientFinalizer             = "storageclient.ocs.openshift.io"
	storageClaimProcessedAnnotationKey = "ocs.openshift.io/storageclaim.processed"
	storageClientDefaultAnnotationKey  = "ocs.openshift.io/storageclient.default"

	// indexes for caching
	ownerIndexName = "index:ownerUID"

	csvPrefix = "ocs-client-operator"
)

// StorageClientReconciler reconciles a StorageClient object
type StorageClientReconciler struct {
	ctx context.Context
	client.Client
	Log           klog.Logger
	Scheme        *runtime.Scheme
	recorder      *utils.EventReporter
	storageClient *v1alpha1.StorageClient

	OperatorNamespace string
}

// SetupWithManager sets up the controller with the Manager.
func (r *StorageClientReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	if err := mgr.GetCache().IndexField(ctx, &v1alpha1.StorageClaim{}, ownerIndexName, func(obj client.Object) []string {
		refs := obj.GetOwnerReferences()
		var owners []string
		for i := range refs {
			owners = append(owners, string(refs[i].UID))
		}
		return owners
	}); err != nil {
		return fmt.Errorf("unable to set up FieldIndexer for StorageClaim's owner uid: %v", err)
	}

	r.recorder = utils.NewEventReporter(mgr.GetEventRecorderFor("controller_storageclient"))
	generationChangePredicate := predicate.GenerationChangedPredicate{}
	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StorageClient{}).
		Owns(&v1alpha1.StorageClaim{}).
		Owns(&batchv1.CronJob{}).
		Owns(&quotav1.ClusterResourceQuota{}, builder.WithPredicates(generationChangePredicate)).
		Owns(&nbv1.NooBaa{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&corev1.Secret{}).
		Owns(&csiopv1a1.CephConnection{}, builder.WithPredicates(generationChangePredicate)).
		Owns(&csiopv1a1.ClientProfileMapping{}, builder.WithPredicates(generationChangePredicate))

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

func (r *StorageClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error
	r.ctx = ctx
	r.Log = log.FromContext(ctx, "StorageClient", req)
	r.Log.Info("Reconciling StorageClient")

	r.storageClient = &v1alpha1.StorageClient{}
	r.storageClient.Name = req.Name
	if err = r.get(r.storageClient); err != nil {
		if kerrors.IsNotFound(err) {
			r.Log.Info("StorageClient resource not found. Ignoring since object must be deleted.")
			return reconcile.Result{}, nil
		}
		r.Log.Error(err, "Failed to get StorageClient.")
		return reconcile.Result{}, fmt.Errorf("failed to get StorageClient: %v", err)
	}

	// Dont Reconcile the StorageClient if it is in failed state
	if r.storageClient.Status.Phase == v1alpha1.StorageClientFailed {
		return reconcile.Result{}, nil
	}

	result, reconcileErr := r.reconcilePhases()

	// Apply status changes to the StorageClient
	statusErr := r.Client.Status().Update(ctx, r.storageClient)
	if statusErr != nil {
		r.Log.Error(statusErr, "Failed to update StorageClient status.")
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

	updateStorageClient := false
	storageClients := &v1alpha1.StorageClientList{}
	if err := r.list(storageClients); err != nil {
		r.Log.Error(err, "unable to list storage clients")
		return ctrl.Result{}, err
	}
	if len(storageClients.Items) == 1 && storageClients.Items[0].Name == r.storageClient.Name {
		if utils.AddAnnotation(r.storageClient, storageClientDefaultAnnotationKey, "true") {
			updateStorageClient = true
		}
	}

	// ensure finalizer
	if controllerutil.AddFinalizer(r.storageClient, storageClientFinalizer) {
		r.storageClient.Status.Phase = v1alpha1.StorageClientInitializing
		r.Log.Info("Finalizer not found for StorageClient. Adding finalizer.", "StorageClient", r.storageClient.Name)
		updateStorageClient = true
	}

	if updateStorageClient {
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
		case "Service":
			serviceSpec := &v1.ServiceSpec{}
			if err := json.Unmarshal(eResource.Data, &serviceSpec); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to unmarshall service spec data: %v", err)
			}
			srv := &v1.Service{}
			srv.Name = eResource.Name
			srv.Namespace = r.OperatorNamespace

			_, err = controllerutil.CreateOrUpdate(r.ctx, r.Client, srv, func() error {
				if err := r.own(srv); err != nil {
					return err
				}

				srv.Spec = *serviceSpec
				return nil
			})
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to create service: %v", err)
			}

		}
	}
	if r.storageClient.GetAnnotations()[storageClaimProcessedAnnotationKey] != "true" {
		if err := r.reconcileBlockStorageClaim(); err != nil {
			return reconcile.Result{}, err
		}

		if err := r.reconcileSharedfileStorageClaim(); err != nil {
			return reconcile.Result{}, err
		}

		utils.AddAnnotation(r.storageClient, storageClaimProcessedAnnotationKey, "true")
		if err := r.update(r.storageClient); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update StorageClient with claim processed annotation: %v", err)
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
	r.Log.Info(fmt.Sprintf("%s successfully %s", obj.GetObjectKind(), result), "name", obj.GetName())
	return nil
}

func (r *StorageClientReconciler) deletionPhase(externalClusterClient *providerClient.OCSProviderClient) (ctrl.Result, error) {
	// TODO Need to take care of deleting the SCC created for this
	// storageClient and also the default SCC created for this storageClient
	r.storageClient.Status.Phase = v1alpha1.StorageClientOffboarding

	if err := r.deleteOwnedStorageClaims(); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to delete storageclaims owned by storageclient %v: %v", r.storageClient.Name, err)
	}
	if err := r.verifyNoStorageClaimsExist(); err != nil {
		r.Log.Error(err, "still storageclaims exist for this storageclient")
		return reconcile.Result{}, fmt.Errorf("still storageclaims exist for this storageclient: %v", err)
	}
	if res, err := r.offboardConsumer(externalClusterClient); err != nil {
		r.Log.Error(err, "Offboarding in progress.")
	} else if !res.IsZero() {
		// result is not empty
		return res, nil
	}

	if controllerutil.RemoveFinalizer(r.storageClient, storageClientFinalizer) {
		r.Log.Info("removing finalizer from StorageClient.", "StorageClient", r.storageClient.Name)
		if err := r.update(r.storageClient); err != nil {
			r.Log.Info("Failed to remove finalizer from StorageClient", "StorageClient", r.storageClient.Name)
			return reconcile.Result{}, fmt.Errorf("failed to remove finalizer from StorageClient: %v", err)
		}
	}
	r.Log.Info("StorageClient is offboarded", "StorageClient", r.storageClient.Name)
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
		r.Log.Error(err, "failed to get the clusterVersion version of the OCP cluster")
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
		r.Log.Error(err, "empty response")
		return reconcile.Result{}, err
	}

	r.storageClient.Status.ConsumerID = response.StorageConsumerUUID
	r.storageClient.Status.Phase = v1alpha1.StorageClientOnboarding

	r.Log.Info("onboarding started")
	return reconcile.Result{Requeue: true}, nil
}

func (r *StorageClientReconciler) acknowledgeOnboarding(externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	_, err := externalClusterClient.AcknowledgeOnboarding(r.ctx, r.storageClient.Status.ConsumerID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			r.logGrpcErrorAndReportEvent(AcknowledgeOnboarding, err, st.Code())
		}
		r.Log.Error(err, "Failed to acknowledge onboarding.")
		return reconcile.Result{}, fmt.Errorf("failed to acknowledge onboarding: %v", err)
	}
	r.storageClient.Status.Phase = v1alpha1.StorageClientConnected

	r.Log.Info("Onboarding is acknowledged successfully.")
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

func (r *StorageClientReconciler) deleteOwnedStorageClaims() error {
	storageClaims := &v1alpha1.StorageClaimList{}
	if err := r.list(storageClaims, client.MatchingFields{ownerIndexName: string(r.storageClient.UID)}); err != nil {
		return fmt.Errorf("failed to list storageClaims via owner reference: %v", err)
	}

	for idx := range storageClaims.Items {
		storageClaim := &storageClaims.Items[idx]
		if err := r.delete(storageClaim); err != nil {
			return fmt.Errorf("failed to delete storageClaim %v: %v", storageClaim.Name, err)
		}
	}
	return nil
}

func (r *StorageClientReconciler) verifyNoStorageClaimsExist() error {

	storageClaims := &v1alpha1.StorageClaimList{}
	if err := r.list(storageClaims); err != nil {
		return fmt.Errorf("failed to list storageClaims: %v", err)
	}

	for idx := range storageClaims.Items {
		storageClaim := &storageClaims.Items[idx]
		if (storageClaim.Spec.StorageClient == "" && r.storageClient.Annotations[storageClientDefaultAnnotationKey] == "true") ||
			storageClaim.Spec.StorageClient == r.storageClient.Name {
			err := fmt.Errorf("failed to cleanup resources. storageClaims are present on the cluster")
			r.recorder.ReportIfNotPresent(r.storageClient, corev1.EventTypeWarning, "Cleanup", err.Error())
			r.Log.Error(err, "Waiting for all storageClaims to be deleted.")
			return err
		}
	}

	return nil
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
		r.Log.Error(err, "StorageProvider:"+grpcCallName+":"+msg)
		r.recorder.ReportIfNotPresent(r.storageClient, eventType, eventReason, msg)
	}
}

func (r *StorageClientReconciler) reconcileClientStatusReporterJob() (reconcile.Result, error) {
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

func (r *StorageClientReconciler) list(obj client.ObjectList, listOptions ...client.ListOption) error {
	return r.Client.List(r.ctx, obj, listOptions...)
}

func (r *StorageClientReconciler) reconcileBlockStorageClaim() error {
	blockClaim := &v1alpha1.StorageClaim{}
	blockClaim.Name = fmt.Sprintf("%s-ceph-rbd", r.storageClient.Name)
	blockClaim.Spec.Type = "block"
	blockClaim.Spec.StorageClient = r.storageClient.Name
	if err := r.own(blockClaim); err != nil {
		return fmt.Errorf("failed to own storageclaim of type block: %v", err)
	}
	if err := r.create(blockClaim); err != nil && !kerrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create block storageclaim: %v", err)
	}
	return nil
}

func (r *StorageClientReconciler) reconcileSharedfileStorageClaim() error {
	sharedfileClaim := &v1alpha1.StorageClaim{}
	sharedfileClaim.Name = fmt.Sprintf("%s-cephfs", r.storageClient.Name)
	sharedfileClaim.Spec.Type = "sharedfile"
	sharedfileClaim.Spec.StorageClient = r.storageClient.Name
	if err := r.own(sharedfileClaim); err != nil {
		return fmt.Errorf("failed to own storageclaim of type sharedfile: %v", err)
	}
	if err := r.create(sharedfileClaim); err != nil && !kerrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create sharedfile storageclaim: %v", err)
	}
	return nil
}

func (r *StorageClientReconciler) get(obj client.Object, opts ...client.GetOption) error {
	key := client.ObjectKeyFromObject(obj)
	return r.Get(r.ctx, key, obj, opts...)
}

func (r *StorageClientReconciler) update(obj client.Object, opts ...client.UpdateOption) error {
	return r.Update(r.ctx, obj, opts...)
}

func (r *StorageClientReconciler) create(obj client.Object, opts ...client.CreateOption) error {
	return r.Create(r.ctx, obj, opts...)
}

func (r *StorageClientReconciler) delete(obj client.Object, opts ...client.DeleteOption) error {
	if err := r.Delete(r.ctx, obj, opts...); err != nil && !kerrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *StorageClientReconciler) own(dependent metav1.Object) error {
	return controllerutil.SetOwnerReference(r.storageClient, dependent, r.Scheme)
}
