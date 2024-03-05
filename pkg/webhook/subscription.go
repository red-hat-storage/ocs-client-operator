package webhook

import (
	"context"
	"fmt"
	"net/http"

	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type SubscriptionValidator struct {
	Client  client.Client
	Decoder *admission.Decoder
}

func (s *SubscriptionValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	subscription := &opv1a1.Subscription{}
	if err := s.Decoder.Decode(req, subscription); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("only subscriptions admission reviews are supported: %v", err))
	}

	if subscription.Spec.Package != "ocs-client-operator" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("only ocs-client-operator subscription validation is supported"))
	}

	storageClients := &v1alpha1.StorageClientList{}
	if err := s.Client.List(ctx, storageClients); err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to list storageclients for validating subscription request: %v", err))
	}

	for idx := range storageClients.Items {
		storageClient := &storageClients.Items[idx]
		if storageClient.Status.DesiredOperatorSubscriptionChannel != subscription.Spec.Channel {
			return admission.Denied(fmt.Sprintf("subscription channel %q not allowed as it'll violate storageclient %q requirements", subscription.Spec.Channel, client.ObjectKeyFromObject(storageClient)))
		}
	}

	return admission.Allowed(fmt.Sprintf("valid subscription channel: %q", subscription.Spec.Channel))
}
