package utils

import (
	"context"
	"fmt"

	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
)

// NewProviderClientForStorageClient creates an OCS provider gRPC client for the given StorageClient.
func NewProviderClientForStorageClient(
	ctx context.Context,
	storageProviderEndpoint string,
) (*providerClient.OCSProviderClient, error) {
	ocsProviderClient, err := providerClient.NewProviderClient(ctx, storageProviderEndpoint, OcsClientTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider client with endpoint %v: %w", storageProviderEndpoint, err)
	}
	return ocsProviderClient, nil
}
