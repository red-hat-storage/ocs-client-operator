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

package main

import (
	"context"
	"math"
	"os"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	configv1 "github.com/openshift/api/config/v1"
	quotav1 "github.com/openshift/api/quota/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	providerclient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	"github.com/red-hat-storage/ocs-operator/services/provider/api/v4/interfaces"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	clusterDNSResourceName = "cluster"
)

func main() {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		klog.Exitf("Failed to add v1alpha1 to scheme: %v", err)
	}

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		klog.Exitf("Failed to add client-go to scheme: %v", err)
	}

	if err := opv1a1.AddToScheme(scheme); err != nil {
		klog.Exitf("Failed to add opv1a1 to scheme: %v", err)
	}

	if err := configv1.AddToScheme(scheme); err != nil {
		klog.Exitf("Failed to add configv1 to scheme: %v", err)
	}

	if err := quotav1.AddToScheme(scheme); err != nil {
		klog.Exitf("Failed to add quotav1 to scheme: %v", err)
	}

	config, err := config.GetConfig()
	if err != nil {
		klog.Exitf("Failed to get config: %v", err)
	}
	cl, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		klog.Exitf("Failed to create controller-runtime client: %v", err)
	}

	ctx := context.Background()

	storageClientName, isSet := os.LookupEnv(utils.StorageClientNameEnvVar)
	if !isSet {
		klog.Exitf("%s env var not set", utils.StorageClientNameEnvVar)
	}

	operatorNamespace, isSet := os.LookupEnv(utils.OperatorNamespaceEnvVar)
	if !isSet {
		klog.Exitf("%s env var not set", utils.OperatorNamespaceEnvVar)
	}

	operatorVersion, isSet := os.LookupEnv(utils.OperatorVersionEnvVar)
	if !isSet {
		klog.Exitf("%s env var not set", utils.OperatorVersionEnvVar)
	}

	storageClient := &v1alpha1.StorageClient{}
	storageClient.Name = storageClientName

	if err = cl.Get(ctx, client.ObjectKeyFromObject(storageClient), storageClient); err != nil {
		klog.Exitf("Failed to get storageClient %q/%q: %v", storageClient.Namespace, storageClient.Name, err)
	}

	if !storageClient.GetDeletionTimestamp().IsZero() {
		klog.Infof("Skipping report, StorageClient %q is being deleted", storageClient.Name)
		os.Exit(0)
	}

	providerClient, err := providerclient.NewProviderClient(
		ctx,
		storageClient.Spec.StorageProviderEndpoint,
		utils.OcsClientTimeout,
	)
	if err != nil {
		klog.Exitf("Failed to create grpc client with endpoint %v: %v", storageClient.Spec.StorageProviderEndpoint, err)
	}
	defer providerClient.Close()

	status := providerclient.NewStorageClientStatus().
		SetClientName(storageClientName).
		SetClientID(string(storageClient.UID)).
		SetOperatorNamespace(operatorNamespace)
	setPlatformInformation(ctx, cl, status)
	setClusterInformation(ctx, cl, status)
	setStorageQuotaUtilizationRatio(ctx, cl, status)

	providerReady := true
	// if GetDesiredClientState RPC is not implemented, it means the provider is not yet upgraded to 4.19
	if _, err := providerClient.GetDesiredClientState(ctx, storageClient.Status.ConsumerID); err != nil {
		if st, ok := grpcstatus.FromError(err); ok && st.Code() == grpccodes.Unimplemented {
			klog.Infof("GetDesiredClientState returned Unimplemented, provider still on 4.18: %v", err)
			providerReady = false
		}
	}
	if providerReady {
		status.SetOperatorVersion(operatorVersion)
	} else {
		// keep the consumer version as 4.18 till the provider is upgraded to 4.19 to avoid making provider not upgradable
		status.SetOperatorVersion("4.18.99")
	}

	statusResponse, err := providerClient.ReportStatus(ctx, storageClient.Status.ConsumerID, status)
	if err != nil {
		klog.Exitf("Failed to report status of storageClient %v: %v", storageClient.Status.ConsumerID, err)
	}

	if utils.AddAnnotation(storageClient, utils.DesiredConfigHashAnnotationKey, statusResponse.DesiredConfigHash) {
		if err := cl.Update(ctx, storageClient); err != nil {
			klog.Exitf("Failed to annotate storageclient %q: %v", storageClient.Name, err)
		}
	}
}

func setClusterInformation(ctx context.Context, cl client.Client, status interfaces.StorageClientStatus) {
	var clusterID configv1.ClusterID
	clusterVersion := &configv1.ClusterVersion{}
	clusterVersion.Name = "version"
	if err := cl.Get(ctx, types.NamespacedName{Name: clusterVersion.Name}, clusterVersion); err != nil {
		klog.Warningf("Failed to get clusterVersion: %v", err)
	} else {
		clusterID = clusterVersion.Spec.ClusterID
	}
	status.SetClusterID(string(clusterID))

	clusterDNS := &configv1.DNS{}
	clusterDNS.Name = clusterDNSResourceName
	if err := cl.Get(ctx, client.ObjectKeyFromObject(clusterDNS), clusterDNS); err != nil {
		klog.Warningf("Failed to get clusterDNS %q: %v", clusterDNS.Name, err)
	}

	if len(clusterDNS.Spec.BaseDomain) == 0 {
		klog.Warningf("Cluster Base Domain is empty.")
	}
	status.SetClusterName(clusterDNS.Spec.BaseDomain)

}

func setStorageQuotaUtilizationRatio(ctx context.Context, cl client.Client, status interfaces.StorageClientStatus) {
	clusterResourceQuota := &quotav1.ClusterResourceQuota{}
	clusterResourceQuota.Name = utils.GetClusterResourceQuotaName(status.GetClientName())

	// No need to check for NotFound because unlimited quota client will not have CRQ resource
	if err := cl.Get(ctx, client.ObjectKeyFromObject(clusterResourceQuota), clusterResourceQuota); client.IgnoreNotFound(err) != nil {
		klog.Warningf("Failed to get clusterResourceQuota %q: %v", clusterResourceQuota.Name, err)
	}

	if clusterResourceQuota.Status.Total.Hard != nil {
		total, totalExists := clusterResourceQuota.Status.Total.Hard["requests.storage"]
		used, usedExists := clusterResourceQuota.Status.Total.Used["requests.storage"]
		if totalExists && usedExists && total.AsApproximateFloat64() > 0 {
			ratio := used.AsApproximateFloat64() / total.AsApproximateFloat64()
			status.SetStorageQuotaUtilizationRatio(math.Min(ratio, 1.0))
		}
	}

}

func setPlatformInformation(ctx context.Context, cl client.Client, status interfaces.StorageClientStatus) {
	var platformVersion string
	clusterVersion := &configv1.ClusterVersion{}
	clusterVersion.Name = "version"
	if err := cl.Get(ctx, client.ObjectKeyFromObject(clusterVersion), clusterVersion); err != nil {
		klog.Warningf("Failed to get clusterVersion: %v", err)
	} else {
		item := utils.Find(clusterVersion.Status.History, func(record *configv1.UpdateHistory) bool {
			return record.State == configv1.CompletedUpdate
		})
		if item != nil {
			platformVersion = item.Version
		}
	}
	if platformVersion == "" {
		klog.Warningf("Unable to find ocp version with completed update")
	}
	status.SetPlatformVersion(platformVersion)
}
