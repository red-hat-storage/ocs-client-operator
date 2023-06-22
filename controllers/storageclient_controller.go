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
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	configv1 "github.com/openshift/api/config/v1"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// grpcCallNames
	OnboardConsumer       = "OnboardConsumer"
	OffboardConsumer      = "OffboardConsumer"
	GetStorageConfig      = "GetStorageConfig"
	AcknowledgeOnboarding = "AcknowledgeOnboarding"

	storageClientLabel          = "ocs.openshift.io/storageclient"
	storageClientNameLabel      = "ocs.openshift.io/storageclient.name"
	storageClientNamespaceLabel = "ocs.openshift.io/storageclient.namespace"
	storageClientFinalizer      = "storageclient.ocs.openshift.io"
)

// StorageClientReconciler reconciles a StorageClient object
type StorageClientReconciler struct {
	ctx context.Context
	client.Client
	Log      klog.Logger
	Scheme   *runtime.Scheme
	recorder *utils.EventReporter

	OperatorNamespace string
}

// SetupWithManager sets up the controller with the Manager.
func (s *StorageClientReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index should be registered before cache start.
	// IndexField is used to filter out the objects that already exists with
	// status.phase != failed This will help in blocking
	// the new storageclient creation if there is already with one with same
	// provider endpoint with status.phase != failed
	_ = mgr.GetCache().IndexField(context.TODO(), &v1alpha1.StorageClient{}, "spec.storageProviderEndpoint", func(o client.Object) []string {
		res := []string{}
		if o.(*v1alpha1.StorageClient).Status.Phase != v1alpha1.StorageClientFailed {
			res = append(res, o.(*v1alpha1.StorageClient).Spec.StorageProviderEndpoint)
		}
		return res
	})
	enqueueStorageClientRequest := handler.EnqueueRequestsFromMapFunc(
		func(obj client.Object) []reconcile.Request {
			annotations := obj.GetAnnotations()
			if _, found := annotations[storageClassClaimAnnotation]; found {
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Name: obj.GetName(),
					},
				}}
			}
			return []reconcile.Request{}
		})
	s.recorder = utils.NewEventReporter(mgr.GetEventRecorderFor("controller_storageclient"))
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StorageClient{}).
		Watches(&source.Kind{Type: &v1alpha1.StorageClassClaim{}}, enqueueStorageClientRequest).
		Complete(s)
}

//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients/finalizers,verbs=update
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch
//+kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;create;update;watch;delete

func (s *StorageClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error
	s.ctx = ctx
	s.Log = log.FromContext(ctx, "StorageClient", req)
	s.Log.Info("Reconciling StorageClient")

	// Fetch the StorageClient instance
	instance := &v1alpha1.StorageClient{}
	instance.Name = req.Name
	instance.Namespace = req.Namespace

	if err = s.Client.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, instance); err != nil {
		if apierrors.IsNotFound(err) {
			s.Log.Info("StorageClient resource not found. Ignoring since object must be deleted.")
			return reconcile.Result{}, nil
		}
		s.Log.Error(err, "Failed to get StorageClient.")
		return reconcile.Result{}, fmt.Errorf("failed to get StorageClient: %v", err)
	}

	// Dont Reconcile the StorageClient if it is in failed state
	if instance.Status.Phase == v1alpha1.StorageClientFailed {
		return reconcile.Result{}, nil
	}

	result, reconcileErr := s.reconcilePhases(instance)

	// Apply status changes to the StorageClient
	statusErr := s.Client.Status().Update(ctx, instance)
	if statusErr != nil {
		s.Log.Error(statusErr, "Failed to update StorageClient status.")
	}
	if reconcileErr != nil {
		err = reconcileErr
	} else if statusErr != nil {
		err = statusErr
	}
	return result, err
}

func (s *StorageClientReconciler) reconcilePhases(instance *v1alpha1.StorageClient) (ctrl.Result, error) {
	storageClientListOption := []client.ListOption{
		client.MatchingFields{"spec.storageProviderEndpoint": instance.Spec.StorageProviderEndpoint},
	}

	storageClientList := &v1alpha1.StorageClientList{}
	if err := s.Client.List(s.ctx, storageClientList, storageClientListOption...); err != nil {
		s.Log.Error(err, "unable to list storage clients")
		return ctrl.Result{}, err
	}

	if len(storageClientList.Items) > 1 {
		s.Log.Info("one StorageClient is allowed per namespace but found more than one. Rejecting new request.")
		instance.Status.Phase = v1alpha1.StorageClientFailed
		// Dont Reconcile again	as there is already a StorageClient with same provider endpoint
		return reconcile.Result{}, nil
	}

	externalClusterClient, err := s.newExternalClusterClient(instance)
	if err != nil {
		return reconcile.Result{}, err
	}
	defer externalClusterClient.Close()

	// deletion phase
	if !instance.GetDeletionTimestamp().IsZero() {
		return s.deletionPhase(instance, externalClusterClient)
	}

	// ensure finalizer
	if !contains(instance.GetFinalizers(), storageClientFinalizer) {
		instance.Status.Phase = v1alpha1.StorageClientInitializing
		s.Log.Info("Finalizer not found for StorageClient. Adding finalizer.", "StorageClient", klog.KRef(instance.Namespace, instance.Name))
		instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers, storageClientFinalizer)
		if err := s.Client.Update(s.ctx, instance); err != nil {
			s.Log.Info("Failed to update StorageClient with finalizer.", "StorageClient", klog.KRef(instance.Namespace, instance.Name))
			return reconcile.Result{}, fmt.Errorf("failed to update StorageClient with finalizer: %v", err)
		}
	}

	if instance.Status.ConsumerID == "" {
		return s.onboardConsumer(instance, externalClusterClient)
	} else if instance.Status.Phase == v1alpha1.StorageClientOnboarding {
		return s.acknowledgeOnboarding(instance, externalClusterClient)
	}

	if res, err := s.reconcileClientStatusReporterJob(instance); err != nil {
		return res, err
	}

	return reconcile.Result{}, nil
}

func (s *StorageClientReconciler) deletionPhase(instance *v1alpha1.StorageClient, externalClusterClient *providerClient.OCSProviderClient) (ctrl.Result, error) {
	// TODO Need to take care of deleting the SCC created for this
	// storageClient and also the default SCC created for this storageClient
	if contains(instance.GetFinalizers(), storageClientFinalizer) {
		instance.Status.Phase = v1alpha1.StorageClientOffboarding
		err := s.verifyNoStorageClassClaimsExist(instance)
		if err != nil {
			s.Log.Error(err, "still storageclassclaims exist for this storageclient")
			return reconcile.Result{}, fmt.Errorf("still storageclassclaims exist for this storageclient: %v", err)
		}
		if res, err := s.offboardConsumer(instance, externalClusterClient); err != nil {
			s.Log.Error(err, "Offboarding in progress.")
		} else if !res.IsZero() {
			// result is not empty
			return res, nil
		}

		cronJob := &batchv1.CronJob{}
		cronJob.Name = getStatusReporterName(instance.Namespace, instance.Name)
		cronJob.Namespace = s.OperatorNamespace

		if err := s.delete(cronJob); err != nil {
			s.Log.Error(err, "Failed to delete the status reporter job")
			return reconcile.Result{}, fmt.Errorf("failed to delete the status reporter job: %v", err)
		}

		s.Log.Info("removing finalizer from StorageClient.", "StorageClient", klog.KRef(instance.Namespace, instance.Name))
		// Once all finalizers have been removed, the object will be deleted
		instance.ObjectMeta.Finalizers = remove(instance.ObjectMeta.Finalizers, storageClientFinalizer)
		if err := s.Client.Update(s.ctx, instance); err != nil {
			s.Log.Info("Failed to remove finalizer from StorageClient", "StorageClient", klog.KRef(instance.Namespace, instance.Name))
			return reconcile.Result{}, fmt.Errorf("failed to remove finalizer from StorageClient: %v", err)
		}
	}
	s.Log.Info("StorageClient is offboarded", "StorageClient", klog.KRef(instance.Namespace, instance.Name))
	return reconcile.Result{}, nil
}

// newExternalClusterClient returns the *providerClient.OCSProviderClient
func (s *StorageClientReconciler) newExternalClusterClient(instance *v1alpha1.StorageClient) (*providerClient.OCSProviderClient, error) {

	ocsProviderClient, err := providerClient.NewProviderClient(
		s.ctx, instance.Spec.StorageProviderEndpoint, time.Second*10)
	if err != nil {
		return nil, fmt.Errorf("failed to create a new provider client: %v", err)
	}

	return ocsProviderClient, nil
}

// onboardConsumer makes an API call to the external storage provider cluster for onboarding
func (s *StorageClientReconciler) onboardConsumer(instance *v1alpha1.StorageClient, externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	// TODO Need to find a way to get rid of ClusterVersion here as it is OCP
	// specific one.
	clusterVersion := &configv1.ClusterVersion{}
	err := s.Client.Get(s.ctx, types.NamespacedName{Name: "version"}, clusterVersion)
	if err != nil {
		s.Log.Error(err, "failed to get the clusterVersion version of the OCP cluster")
		return reconcile.Result{}, fmt.Errorf("failed to get the clusterVersion version of the OCP cluster: %v", err)
	}

	name := fmt.Sprintf("storageconsumer-%s", clusterVersion.Spec.ClusterID)
	response, err := externalClusterClient.OnboardConsumer(
		s.ctx, instance.Spec.OnboardingTicket, name)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			s.logGrpcErrorAndReportEvent(instance, OnboardConsumer, err, st.Code())
		}
		return reconcile.Result{}, fmt.Errorf("failed to onboard consumer: %v", err)
	}

	if response.StorageConsumerUUID == "" {
		err = fmt.Errorf("storage provider response is empty")
		s.Log.Error(err, "empty response")
		return reconcile.Result{}, err
	}

	instance.Status.ConsumerID = response.StorageConsumerUUID
	instance.Status.Phase = v1alpha1.StorageClientOnboarding

	s.Log.Info("onboarding started")
	return reconcile.Result{Requeue: true}, nil
}

func (s *StorageClientReconciler) acknowledgeOnboarding(instance *v1alpha1.StorageClient, externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	_, err := externalClusterClient.AcknowledgeOnboarding(s.ctx, instance.Status.ConsumerID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			s.logGrpcErrorAndReportEvent(instance, AcknowledgeOnboarding, err, st.Code())
		}
		s.Log.Error(err, "Failed to acknowledge onboarding.")
		return reconcile.Result{}, fmt.Errorf("failed to acknowledge onboarding: %v", err)
	}
	instance.Status.Phase = v1alpha1.StorageClientConnected

	s.Log.Info("Onboarding is acknowledged successfully.")
	return reconcile.Result{Requeue: true}, nil
}

// offboardConsumer makes an API call to the external storage provider cluster for offboarding
func (s *StorageClientReconciler) offboardConsumer(instance *v1alpha1.StorageClient, externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	_, err := externalClusterClient.OffboardConsumer(s.ctx, instance.Status.ConsumerID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			s.logGrpcErrorAndReportEvent(instance, OffboardConsumer, err, st.Code())
		}
		return reconcile.Result{}, fmt.Errorf("failed to offboard consumer: %v", err)
	}

	return reconcile.Result{}, nil
}

func (s *StorageClientReconciler) verifyNoStorageClassClaimsExist(instance *v1alpha1.StorageClient) error {

	storageClassClaims := &v1alpha1.StorageClassClaimList{}
	err := s.Client.List(s.ctx, storageClassClaims)
	if err != nil {
		return fmt.Errorf("failed to list storageClassClaims: %v", err)
	}

	for i := range storageClassClaims.Items {
		storageClassClaim := &storageClassClaims.Items[i]
		sc := storageClassClaim.Labels[storageClientLabel]
		if sc == instance.Name {
			err = fmt.Errorf("Failed to cleanup resources. storageClassClaims are present." +
				"Delete all storageClassClaims for the cleanup to proceed")
			s.recorder.ReportIfNotPresent(instance, corev1.EventTypeWarning, "Cleanup", err.Error())
			s.Log.Error(err, "Waiting for all storageClassClaims to be deleted.")
			return err
		}
	}

	return nil
}
func (s *StorageClientReconciler) logGrpcErrorAndReportEvent(instance *v1alpha1.StorageClient, grpcCallName string, err error, errCode codes.Code) {

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
		s.Log.Error(err, "StorageProvider:"+grpcCallName+":"+msg)
		s.recorder.ReportIfNotPresent(instance, eventType, eventReason, msg)
	}
}

func getStatusReporterName(namespace, name string) string {
	// getStatusReporterName generates a name for a StatusReporter CronJob.
	var s struct {
		StorageClientName      string `json:"storageClientName"`
		StorageClientNamespace string `json:"storageClientNamespace"`
	}
	s.StorageClientName = name
	s.StorageClientNamespace = namespace

	statusReporterName, err := json.Marshal(s)
	if err != nil {
		klog.Errorf("failed to marshal a name for a storage client based on %v. %v", s, err)
		panic("failed to marshal storage client name")
	}
	reporterName := md5.Sum([]byte(statusReporterName))
	// The name of the StorageClient is the MD5 hash of the JSON
	// representation of the StorageClient name and namespace.
	return fmt.Sprintf("storageclient-%s-status-reporter", hex.EncodeToString(reporterName[:8]))
}

// addLabel add a label to a resource metadata
func addLabel(obj metav1.Object, key string, value string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}
	labels[key] = value
}

func (s *StorageClientReconciler) delete(obj client.Object) error {
	if err := s.Client.Delete(s.ctx, obj); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func (s *StorageClientReconciler) reconcileClientStatusReporterJob(instance *v1alpha1.StorageClient) (reconcile.Result, error) {
	// start the cronJob to ping the provider api server
	cronJob := &batchv1.CronJob{}
	cronJob.Name = getStatusReporterName(instance.Namespace, instance.Name)
	cronJob.Namespace = s.OperatorNamespace
	addLabel(cronJob, storageClientNameLabel, instance.Name)
	addLabel(cronJob, storageClientNamespaceLabel, instance.Namespace)

	_, err := controllerutil.CreateOrUpdate(s.ctx, s.Client, cronJob, func() error {
		cronJob.Spec = batchv1.CronJobSpec{
			Schedule: "* * * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "heartbeat",
									Image: os.Getenv(utils.StatusReporterImageEnvVar),
									Command: []string{
										"/status-reporter",
									},
									Env: []corev1.EnvVar{
										{
											Name:  utils.StorageClientNamespaceEnvVar,
											Value: instance.Namespace,
										},
										{
											Name:  utils.StorageClientNameEnvVar,
											Value: instance.Name,
										},
										{
											Name:  utils.OperatorNamespaceEnvVar,
											Value: s.OperatorNamespace,
										},
									},
								},
							},
							RestartPolicy:      corev1.RestartPolicyOnFailure,
							ServiceAccountName: "ocs-client-operator-status-reporter",
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
