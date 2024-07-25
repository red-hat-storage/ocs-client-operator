package webhook

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-logr/logr"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type SubscriptionAdmission struct {
	Client  client.Client
	Decoder admission.Decoder
	Log     logr.Logger
}

func (s *SubscriptionAdmission) Handle(ctx context.Context, req admission.Request) admission.Response {
	s.Log.Info("Request received for admission review")

	// review should be for a subscription
	subscription := &opv1a1.Subscription{}
	if err := s.Decoder.Decode(req, subscription); err != nil {
		s.Log.Error(err, "failed to decode admission review as subscription")
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("only subscriptions admission reviews are supported: %v", err))
	}

	// review should be for ocs-client-operator subscription
	if subscription.Spec.Package != "ocs-client-operator" {
		s.Log.Info("subscription package is not 'ocs-client-operator'")
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("only ocs-client-operator subscription validation is supported"))
	}

	storageClients := &v1alpha1.StorageClientList{}
	if err := s.Client.List(ctx, storageClients); err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to list storageclients for validating subscription request: %v", err))
	}

	// this is the channel that user want to subscribe to
	requestedSubcriptionChannel := subscription.Spec.Channel
	for idx := range storageClients.Items {
		storageClient := &storageClients.Items[idx]
		namespacedName := client.ObjectKeyFromObject(storageClient)

		annotations := storageClient.GetAnnotations()
		if annotations != nil {
			allowedSubscriptionChannel, exist := annotations[utils.DesiredSubscriptionChannelAnnotationKey]
			if exist && allowedSubscriptionChannel != requestedSubcriptionChannel {
				s.Log.Info(fmt.Sprintf("Rejecting review as it doesn't conform to storageclient %q desired subscription channel", namespacedName))
				return admission.Denied(fmt.Sprintf("subscription channel %q not allowed as it'll violate storageclient %q requirements", requestedSubcriptionChannel, namespacedName))
			}
		}
	}

	s.Log.Info("Allowing review request as it doesn't violate any storageclients (if exist) desired subscription channel")
	return admission.Allowed(fmt.Sprintf("valid subscription channel: %q", subscription.Spec.Channel))
}
