package webhook

import (
	"context"
	"fmt"
	"net/http"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	rbdFormatWithSuffix    = "%s-ceph-rbd"
	cephFsFormatWithSuffix = "%s-cephfs"
)

type StorageClaimAdmission struct {
	Client  client.Client
	Decoder admission.Decoder
	Log     logr.Logger
}

func (s *StorageClaimAdmission) Handle(ctx context.Context, req admission.Request) admission.Response {
	s.Log.Info("Request received for admission review")

	// review should be for a storageClaim
	storageClaim := &v1alpha1.StorageClaim{}
	if err := s.Decoder.Decode(req, storageClaim); err != nil {
		s.Log.Error(err, "failed to decode admission review as storageclaim")
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("only storageclaims admission reviews are supported: %v", err))
	}

	storageClients := &v1alpha1.StorageClientList{}
	if err := s.Client.List(ctx, storageClients); err != nil {
		s.Log.Error(err, "failed to list storageclients")
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to list storageclients for validating subscription request: %v", err))
	}

	for idx := range storageClients.Items {
		if storageClients.Items[idx].Status.Phase != v1alpha1.StorageClientFailed {
			clientName := storageClients.Items[idx].Name
			supportedRbdClaim := fmt.Sprintf(rbdFormatWithSuffix, clientName)
			supportedCephFsClaim := fmt.Sprintf(cephFsFormatWithSuffix, clientName)
			if storageClaim.Name == supportedRbdClaim || storageClaim.Name == supportedCephFsClaim {
				s.Log.Info("Allowing review request as the storageclaim has one of the supported names")
				return admission.Allowed("valid storageclaim")
			}
		}
	}

	s.Log.Info("Rejecting review request as the storageclaim name doesn't match any of the supported names")
	return admission.Denied(fmt.Sprintf("unsupported storageclaim with name %s", storageClaim.Name))
}
