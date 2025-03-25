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
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	configv1 "github.com/openshift/api/config/v1"
	quotav1 "github.com/openshift/api/quota/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	providerclient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	"github.com/red-hat-storage/ocs-operator/services/provider/api/v4/interfaces"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	csvPrefix              = "ocs-client-operator"
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

	if err := csiopv1a1.AddToScheme(scheme); err != nil {
		klog.Exitf("Failed to add csiopv1a1 to scheme: %v", err)
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
	storageClient := &v1alpha1.StorageClient{}
	storageClient.Name = storageClientName

	if err = cl.Get(ctx, client.ObjectKeyFromObject(storageClient), storageClient); err != nil {
		klog.Exitf("Failed to get storageClient %q/%q: %v", storageClient.Namespace, storageClient.Name, err)
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
		SetClientID(string(storageClient.UID))
	setPlatformInformation(ctx, cl, status)
	setOperatorInformation(ctx, cl, status, operatorNamespace)
	setClusterInformation(ctx, cl, status)
	setStorageQuotaUtilizationRatio(ctx, cl, status)
	statusResponse, err := providerClient.ReportStatus(ctx, storageClient.Status.ConsumerID, status)
	if err != nil {
		klog.Exitf("Failed to report status of storageClient %v: %v", storageClient.Status.ConsumerID, err)
	}

	updated := false
	if utils.AddAnnotation(storageClient, utils.DesiredSubscriptionChannelAnnotationKey, statusResponse.DesiredClientOperatorChannel) {
		updated = true
	}

	if utils.AddAnnotation(storageClient, utils.DesiredConfigHashAnnotationKey, statusResponse.DesiredConfigHash) {
		updated = true
	}

	if updated {
		if err := cl.Update(ctx, storageClient); err != nil {
			klog.Exitf("Failed to annotate storageclient %q: %v", storageClient.Name, err)
		}
	}

	if err := updateCSIConfig(ctx, cl, providerClient, storageClient, operatorNamespace); err != nil {
		klog.Exitf("Failed to update csi config: %v", err)
	}
}

func updateCSIConfig(ctx context.Context,
	cl client.Client,
	providerClient *providerclient.OCSProviderClient,
	storageClient *v1alpha1.StorageClient,
	operatorNamespace string) error {
	scResponse, err := providerClient.GetStorageConfig(ctx, storageClient.Status.ConsumerID)
	if err != nil {
		return fmt.Errorf("failed to get StorageConfig of storageClient %v: %v", storageClient.Status.ConsumerID, err)
	}
	for _, eResource := range scResponse.ExternalResource {
		if eResource.Kind == "CephConnection" {
			desiredCephConnectionSpec := &csiopv1a1.CephConnectionSpec{}
			if err := json.Unmarshal(eResource.Data, &desiredCephConnectionSpec); err != nil {
				return fmt.Errorf("failed to unmarshall cephConnectionSpec: %v", err)
			}

			cephConnection := &csiopv1a1.CephConnection{}
			cephConnection.Name = storageClient.Name
			cephConnection.Namespace = operatorNamespace
			if err := cl.Get(ctx, client.ObjectKeyFromObject(cephConnection), cephConnection); err != nil {
				return fmt.Errorf("failed to get csi cephConnection resource: %v", err)
			}

			if !reflect.DeepEqual(cephConnection.Spec, desiredCephConnectionSpec) {
				cephConnectionCopy := &csiopv1a1.CephConnection{}
				cephConnection.ObjectMeta.DeepCopyInto(&cephConnectionCopy.ObjectMeta)
				desiredCephConnectionSpec.DeepCopyInto(&cephConnectionCopy.Spec)
				// patch is being used to ensure spec is accurate at this point of time even if there are reconciles happening which change resourceversion
				if err := cl.Patch(ctx, cephConnection, client.MergeFrom(cephConnectionCopy)); err != nil {
					return fmt.Errorf("failed to patch csi cephConnectionSpec: %v", err)
				}
			}
		}
	}

	return nil
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

func setOperatorInformation(ctx context.Context, cl client.Client, status interfaces.StorageClientStatus,
	operatorNamespace string) {
	var operatorVersion string
	csvList := opv1a1.ClusterServiceVersionList{}
	if err := cl.List(ctx, &csvList, client.InNamespace(operatorNamespace)); err != nil {
		klog.Warningf("Failed to list csv resources: %v", err)
	} else {
		item := utils.Find(csvList.Items, func(csv *opv1a1.ClusterServiceVersion) bool {
			return strings.HasPrefix(csv.Name, csvPrefix)
		})
		if item != nil {
			operatorVersion = item.Spec.Version.String()
		}
	}
	if operatorVersion == "" {
		klog.Warningf("Unable to find csv with prefix %q", csvPrefix)
	}
	status.
		SetOperatorVersion(operatorVersion).
		SetOperatorNamespace(operatorNamespace)
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
