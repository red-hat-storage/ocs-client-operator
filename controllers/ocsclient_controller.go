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
	"fmt"
	"strings"
	"time"

	v1alpha1 "github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"

	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/client"

	configv1 "github.com/openshift/api/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// grpcCallNames
	OnboardConsumer       = "OnboardConsumer"
	OffboardConsumer      = "OffboardConsumer"
	UpdateCapacity        = "UpdateCapacity"
	GetStorageConfig      = "GetStorageConfig"
	AcknowledgeOnboarding = "AcknowledgeOnboarding"

	ocsClientAnnotation = "odf.openshift.io/ocsclient"
	ocsClientFinalizer  = "ocsclient.odf.openshift.io"
)

// OcsClientReconciler reconciles a OcsClient object
type OcsClientReconciler struct {
	client.Client
	Log    klog.Logger
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=odf.openshift.io,resources=ocsclients,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=odf.openshift.io,resources=ocsclients/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=odf.openshift.io,resources=ocsclients/finalizers,verbs=update
//+kubebuilder:rbac:groups=odf.openshift.io,resources=storageclassclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=odf.openshift.io,resources=storageclassclaims/status,verbs=get;update;patch

// SetupWithManager sets up the controller with the Manager.
func (r *OcsClientReconciler) SetupWithManager(mgr ctrl.Manager) error {
	annotationMapFunc := handler.EnqueueRequestsFromMapFunc(
		func(obj client.Object) []reconcile.Request {
			annotations := obj.GetAnnotations()
			if annotation, found := annotations[ocsClientAnnotation]; found {
				parts := strings.Split(annotation, "/")
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: parts[0],
						Name:      parts[1],
					},
				}}
			}
			return []reconcile.Request{}
		})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.OcsClient{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
		)).
		Watches(&source.Kind{Type: &v1alpha1.StorageClassClaim{}}, annotationMapFunc).
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *OcsClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error

	r.Log = log.FromContext(ctx, "OcsClient", req)
	r.Log.Info("Reconciling OcsClient")

	// Fetch the OcsClient instance
	instance := &v1alpha1.OcsClient{}
	instance.Name = req.Name
	instance.Namespace = req.Namespace

	if err = r.Client.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, instance); err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("OcsClient resource not found. Ignoring since object must be deleted.")
			return reconcile.Result{}, nil
		}
		r.Log.Error(err, "Failed to get OcsClient.")
		return reconcile.Result{}, err
	}

	result, reconcileErr := r.reconcilePhases(instance)

	// Apply status changes to the OcsClient
	statusErr := r.Client.Status().Update(ctx, instance)
	if statusErr != nil {
		r.Log.Info("Failed to update OcsClient status.")
	}

	if reconcileErr != nil {
		err = reconcileErr
	} else if statusErr != nil {
		err = statusErr
	}
	return result, err
}

func (r *OcsClientReconciler) reconcilePhases(instance *v1alpha1.OcsClient) (ctrl.Result, error) {
	externalClusterClient, err := r.newExternalClusterClient(instance)
	if err != nil {
		return reconcile.Result{}, err
	}
	defer externalClusterClient.Close()

	// deletion phase
	if !instance.GetDeletionTimestamp().IsZero() {
		return r.deletionPhase(instance, externalClusterClient)
	}

	instance.Status.Phase = v1alpha1.OcsClientInitializing

	// ensure finalizer
	if !contains(instance.GetFinalizers(), ocsClientFinalizer) {
		r.Log.Info("Finalizer not found for OcsClient. Adding finalizer.", "OcsClient", klog.KRef(instance.Namespace, instance.Name))
		instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers, ocsClientFinalizer)
		if err := r.Client.Update(context.TODO(), instance); err != nil {
			r.Log.Info("Failed to update OcsClient with finalizer.", "OcsClient", klog.KRef(instance.Namespace, instance.Name))
			return reconcile.Result{}, err
		}
	}

	if instance.Status.ConsumerID == "" {
		return r.onboardConsumer(instance, externalClusterClient)
	} else if instance.Status.Phase == v1alpha1.OcsClientOnboarding {
		return r.acknowledgeOnboarding(instance, externalClusterClient)
	} else if !instance.Spec.RequestedCapacity.Equal(instance.Status.GrantedCapacity) {
		res, err := r.updateConsumerCapacity(instance, externalClusterClient)
		if err != nil || !res.IsZero() {
			return res, err
		}
	}

	return reconcile.Result{}, nil
}

func (r *OcsClientReconciler) deletionPhase(instance *v1alpha1.OcsClient, externalClusterClient *providerClient.OCSProviderClient) (ctrl.Result, error) {
	if contains(instance.GetFinalizers(), ocsClientFinalizer) {
		instance.Status.Phase = v1alpha1.OcsClientOffboarding

		if res, err := r.offboardConsumer(instance, externalClusterClient); err != nil {
			r.Log.Info("Offboarding in progress.", "Status", err)
			//r.recorder.ReportIfNotPresent(instance, corev1.EventTypeWarning, statusutil.EventReasonUninstallPending, err.Error())
			return reconcile.Result{RequeueAfter: time.Second * time.Duration(1)}, nil
		} else if !res.IsZero() {
			// result is not empty
			return res, nil
		}
		r.Log.Info("removing finalizer from OcsClient.", "OcsClient", klog.KRef(instance.Namespace, instance.Name))
		// Once all finalizers have been removed, the object will be deleted
		instance.ObjectMeta.Finalizers = remove(instance.ObjectMeta.Finalizers, ocsClientFinalizer)
		if err := r.Client.Update(context.TODO(), instance); err != nil {
			r.Log.Info("Failed to remove finalizer from OcsClient", "OcsClient", klog.KRef(instance.Namespace, instance.Name))
			return reconcile.Result{}, err
		}
	}
	r.Log.Info("OcsClient is offboarded", "OcsClient", klog.KRef(instance.Namespace, instance.Name))
	//returnErr := r.SetOperatorConditions("Skipping OcsClient reconciliation", "Terminated", metav1.ConditionTrue, nil)
	return reconcile.Result{}, nil
}

// newExternalClusterClient returns the *providerClient.OCSProviderClient
func (r *OcsClientReconciler) newExternalClusterClient(instance *v1alpha1.OcsClient) (*providerClient.OCSProviderClient, error) {

	ocsProviderClient, err := providerClient.NewProviderClient(
		context.Background(), instance.Spec.StorageProviderEndpoint, time.Second*10)
	if err != nil {
		return nil, err
	}

	return ocsProviderClient, nil
}

// onboardConsumer makes an API call to the external storage provider cluster for onboarding
func (r *OcsClientReconciler) onboardConsumer(instance *v1alpha1.OcsClient, externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	clusterVersion := &configv1.ClusterVersion{}
	err := r.Client.Get(context.Background(), types.NamespacedName{Name: "version"}, clusterVersion)
	if err != nil {
		r.Log.Error(err, "failed to get the clusterVersion version of the OCP cluster")
		return reconcile.Result{}, err
	}

	name := fmt.Sprintf("ocsclient-%s-%s", instance.Name, clusterVersion.Spec.ClusterID)
	response, err := externalClusterClient.OnboardConsumer(
		context.Background(), instance.Spec.OnboardingTicket, name,
		instance.Spec.RequestedCapacity.String())
	if err != nil {
		if s, ok := status.FromError(err); ok {
			r.logGrpcErrorAndReportEvent(instance, OnboardConsumer, err, s.Code())
		}
		return reconcile.Result{}, err
	}

	if response.StorageConsumerUUID == "" || response.GrantedCapacity == "" {
		err = fmt.Errorf("storage provider response is empty")
		r.Log.Error(err, "empty response")
		return reconcile.Result{}, err
	}

	instance.Status.ConsumerID = response.StorageConsumerUUID
	instance.Status.GrantedCapacity = resource.MustParse(response.GrantedCapacity)
	instance.Status.Phase = v1alpha1.OcsClientOnboarding

	r.Log.Info("onboarding complete")
	return reconcile.Result{Requeue: true}, nil
}

func (r *OcsClientReconciler) acknowledgeOnboarding(instance *v1alpha1.OcsClient, externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	_, err := externalClusterClient.AcknowledgeOnboarding(context.Background(), instance.Status.ConsumerID)
	if err != nil {
		if s, ok := status.FromError(err); ok {
			r.logGrpcErrorAndReportEvent(instance, AcknowledgeOnboarding, err, s.Code())
		}
		r.Log.Error(err, "External-OCS:Failed to acknowledge onboarding.")
		return reconcile.Result{}, err
	}

	// claims should be created only once and should not be created/updated again if user deletes/update it.
	err = r.createDefaultStorageClassClaims(instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	// instance.Status.Phase = statusutil.PhaseProgressing

	r.Log.Info("External-OCS:Onboarding is acknowledged successfully.")
	return reconcile.Result{Requeue: true}, nil
}

// offboardConsumer makes an API call to the external storage provider cluster for offboarding
func (r *OcsClientReconciler) offboardConsumer(instance *v1alpha1.OcsClient, externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {

	_, err := externalClusterClient.OffboardConsumer(context.Background(), instance.Status.ConsumerID)
	if err != nil {
		if s, ok := status.FromError(err); ok {
			r.logGrpcErrorAndReportEvent(instance, OffboardConsumer, err, s.Code())
		}
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// updateConsumerCapacity makes an API call to the external storage provider cluster to update the capacity
func (r *OcsClientReconciler) updateConsumerCapacity(instance *v1alpha1.OcsClient, externalClusterClient *providerClient.OCSProviderClient) (reconcile.Result, error) {
	instance.Status.Phase = v1alpha1.OcsClientUpdating

	response, err := externalClusterClient.UpdateCapacity(
		context.Background(),
		instance.Status.ConsumerID,
		instance.Spec.RequestedCapacity.String())
	if err != nil {
		if s, ok := status.FromError(err); ok {
			r.logGrpcErrorAndReportEvent(instance, UpdateCapacity, err, s.Code())
		}
		return reconcile.Result{}, err
	}

	responseQuantity, err := resource.ParseQuantity(response.GrantedCapacity)
	if err != nil {
		r.Log.Error(err, "Failed to parse GrantedCapacity from UpdateCapacity response.", "GrantedCapacity", response.GrantedCapacity)
		return reconcile.Result{}, err
	}

	if !instance.Spec.RequestedCapacity.Equal(responseQuantity) {
		klog.Warningf("GrantedCapacity is not equal to the RequestedCapacity in the UpdateCapacity response.",
			"GrantedCapacity", response.GrantedCapacity, "RequestedCapacity", instance.Spec.RequestedCapacity)
	}

	instance.Status.GrantedCapacity = responseQuantity
	instance.Status.Phase = v1alpha1.OcsClientConnected

	return reconcile.Result{}, nil
}

/*
// getExternalConfigFromProvider makes an API call to the external storage provider cluster for json blob
func (r *OcsClientReconciler) getExternalConfigFromProvider(
	instance *v1alpha1.OcsClient, externalClusterClient *providerClient.OCSProviderClient) ([]ExternalResource, reconcile.Result, error) {

	response, err := externalClusterClient.GetStorageConfig(context.Background(), instance.Status.ConsumerID)
	if err != nil {
		if s, ok := status.FromError(err); ok {
			r.logGrpcErrorAndReportEvent(instance, GetStorageConfig, err, s.Code())

			// storage consumer is not ready yet, requeue after some time
			if s.Code() == codes.Unavailable {
				return nil, reconcile.Result{RequeueAfter: time.Second * 5}, nil
			}
		}

		return nil, reconcile.Result{}, err
	}

	var externalResources []ExternalResource

	for _, eResource := range response.ExternalResource {

		data := map[string]string{}
		err = json.Unmarshal(eResource.Data, &data)
		if err != nil {
			r.Log.Error(err, "Failed to Unmarshal response of GetStorageConfig", "Kind", eResource.Kind, "Name", eResource.Name, "Data", eResource.Data)
			return nil, reconcile.Result{}, err
		}

		externalResources = append(externalResources, ExternalResource{
			Kind: eResource.Kind,
			Data: data,
			Name: eResource.Name,
		})
	}

	return externalResources, reconcile.Result{}, nil
}
*/
func (r *OcsClientReconciler) logGrpcErrorAndReportEvent(instance *v1alpha1.OcsClient, grpcCallName string, err error, errCode codes.Code) {

	// var msg, eventReason, eventType string
	var msg string

	if grpcCallName == OnboardConsumer {
		if errCode == codes.InvalidArgument {
			msg = "Token is invalid. Verify the token again or contact the provider admin"
			//eventReason = "TokenInvalid"
			//eventType = corev1.EventTypeWarning
		} else if errCode == codes.AlreadyExists {
			msg = "Token is already used. Contact provider admin for a new token"
			//eventReason = "TokenAlreadyUsed"
			//eventType = corev1.EventTypeWarning
		}
	} else if grpcCallName == AcknowledgeOnboarding {
		if errCode == codes.NotFound {
			msg = "StorageConsumer not found. Contact the provider admin"
			//eventReason = "NotFound"
			//eventType = corev1.EventTypeWarning
		}
	} else if grpcCallName == OffboardConsumer {
		if errCode == codes.InvalidArgument {
			msg = "StorageConsumer UID is not valid. Contact the provider admin"
			//eventReason = "UIDInvalid"
			//eventType = corev1.EventTypeWarning
		}
	} else if grpcCallName == UpdateCapacity {
		if errCode == codes.InvalidArgument {
			msg = "StorageConsumer UID or requested capacity is not valid. Contact the provider admin"
			//eventReason = "UIDorCapacityInvalid"
			//eventType = corev1.EventTypeWarning
		} else if errCode == codes.NotFound {
			msg = "StorageConsumer UID not found. Contact the provider admin"
			//eventReason = "UIDNotFound"
			//eventType = corev1.EventTypeWarning
		}
	} else if grpcCallName == GetStorageConfig {
		if errCode == codes.InvalidArgument {
			msg = "StorageConsumer UID is not valid. Contact the provider admin"
			//eventReason = "UIDInvalid"
			//eventType = corev1.EventTypeWarning
		} else if errCode == codes.NotFound {
			msg = "StorageConsumer UID not found. Contact the provider admin"
			//eventReason = "UIDNotFound"
			//eventType = corev1.EventTypeWarning
		} else if errCode == codes.Unavailable {
			msg = "StorageConsumer is not ready yet. Will requeue after 5 second"
			//eventReason = "NotReady"
			//eventType = corev1.EventTypeNormal
		}
	}

	if msg != "" {
		r.Log.Error(err, "StorageProvider:"+grpcCallName+":"+msg)
		// r.recorder.ReportIfNotPresent(instance, eventType, eventReason, msg)
	}
}

func (r *OcsClientReconciler) createAndOwnStorageClassClaim(instance *v1alpha1.OcsClient, claim *v1alpha1.StorageClassClaim) error {

	err := controllerutil.SetOwnerReference(instance, claim, r.Client.Scheme())
	if err != nil {
		return err
	}

	claim.Annotations = map[string]string{
		ocsClientAnnotation: fmt.Sprintf("%s/%s", instance.Namespace, instance.Name),
	}

	err = r.Client.Create(context.TODO(), claim)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// claims should be created only once and should not be created/updated again if user deletes/update it.
func (r *OcsClientReconciler) createDefaultStorageClassClaims(instance *v1alpha1.OcsClient) error {

	storageClassClaimFile := &v1alpha1.StorageClassClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateNameForCephFilesystemSC(instance.Name),
			Namespace: instance.Namespace,
			Labels:    map[string]string{
				//defaultStorageClassClaimLabel: "true",
			},
		},
		Spec: v1alpha1.StorageClassClaimSpec{
			Type: "sharedfilesystem",
		},
	}

	storageClassClaimBlock := &v1alpha1.StorageClassClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateNameForCephBlockPoolSC(instance.Name),
			Namespace: instance.Namespace,
			Labels:    map[string]string{
				//defaultStorageClassClaimLabel: "true",
			},
		},
		Spec: v1alpha1.StorageClassClaimSpec{
			Type: "blockpool",
		},
	}

	err := r.createAndOwnStorageClassClaim(instance, storageClassClaimFile)
	if err != nil {
		return err
	}

	err = r.createAndOwnStorageClassClaim(instance, storageClassClaimBlock)
	if err != nil {
		return err
	}

	return nil
}
