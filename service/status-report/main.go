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
	"os"
	"strings"
	"time"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/csi"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	configv1 "github.com/openshift/api/config/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	providerclient "github.com/red-hat-storage/ocs-operator/v4/services/provider/client"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	csvPrefix = "ocs-client-operator"
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

	config, err := config.GetConfig()
	if err != nil {
		klog.Exitf("Failed to get config: %v", err)
	}
	cl, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		klog.Exitf("Failed to create controller-runtime client: %v", err)
	}

	ctx := context.Background()

	storageClientNamespace, isSet := os.LookupEnv(utils.StorageClientNamespaceEnvVar)
	if !isSet {
		klog.Exitf("%s env var not set", utils.StorageClientNamespaceEnvVar)
	}

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
	storageClient.Namespace = storageClientNamespace

	if err = cl.Get(ctx, types.NamespacedName{Name: storageClient.Name, Namespace: storageClient.Namespace}, storageClient); err != nil {
		klog.Exitf("Failed to get storageClient %q/%q: %v", storageClient.Namespace, storageClient.Name, err)
	}

	var oprVersion string
	csvList := opv1a1.ClusterServiceVersionList{}
	if err = cl.List(ctx, &csvList, client.InNamespace(storageClientNamespace)); err != nil {
		klog.Warningf("Failed to list csv resources: %v", err)
	} else {
		item := utils.Find(csvList.Items, func(csv *opv1a1.ClusterServiceVersion) bool {
			return strings.HasPrefix(csv.Name, csvPrefix)
		})
		if item != nil {
			oprVersion = item.Spec.Version.String()
		}
	}
	if oprVersion == "" {
		klog.Warningf("Unable to find csv with prefix %q", csvPrefix)
	}

	var pltVersion string
	clusterVersion := &configv1.ClusterVersion{}
	clusterVersion.Name = "version"
	if err = cl.Get(ctx, types.NamespacedName{Name: clusterVersion.Name}, clusterVersion); err != nil {
		klog.Warningf("Failed to get clusterVersion: %v", err)
	} else {
		item := utils.Find(clusterVersion.Status.History, func(record *configv1.UpdateHistory) bool {
			return record.State == configv1.CompletedUpdate
		})
		if item != nil {
			pltVersion = item.Version
		}
	}
	if pltVersion == "" {
		klog.Warningf("Unable to find ocp version with completed update")
	}

	providerClient, err := providerclient.NewProviderClient(
		ctx,
		storageClient.Spec.StorageProviderEndpoint,
		10*time.Second,
	)
	if err != nil {
		klog.Exitf("Failed to create grpc client: %v", err)
	}
	defer providerClient.Close()

	status := providerclient.NewStorageClientStatus().
		SetPlatformVersion(pltVersion).
		SetOperatorVersion(oprVersion)
	statusResponse, err := providerClient.ReportStatus(ctx, storageClient.Status.ConsumerID, status)
	if err != nil {
		klog.Exitf("Failed to report status of storageClient %v: %v", storageClient.Status.ConsumerID, err)
	}

	// we need consumerID in the status field and take a copy as failed status updates clears that field subsequently
	consumerID := storageClient.Status.ConsumerID

	storageClient.Status = v1alpha1.StorageClientStatus{
		DesiredOperatorSubscriptionChannel: statusResponse.GetDesiredClientOperatorChannel(),
	}
	jsonPatch, err := json.Marshal(storageClient)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal storageclient cr: %v", err))
	}

	// patch is being used over update as other fields in status maybe set by storageclient controller in parallel.
	// mergepatch is being used over jsonpatch as status.desiredOperatorSubscriptionChannel is initially not present and
	//	jsonpatch would've required a branch for "add" or "replace" op.
	// marshal of status above only encodes desiredOperatorSubscriptionChannel as other fields are set to "omitempty" and so mergepatch doesn't overwrite other fields
	if err = cl.Status().Patch(ctx, storageClient, client.RawPatch(types.MergePatchType, jsonPatch)); err != nil {
		klog.Errorf("Failed to patch storageclient %q desired operator subscription channel: %v", storageClient.Name, err)
	}

	var csiClusterConfigEntry = new(csi.ClusterConfigEntry)
	scResponse, err := providerClient.GetStorageConfig(ctx, consumerID)
	if err != nil {
		klog.Exitf("Failed to get StorageConfig of storageClient %v: %v", consumerID, err)
	}
	for _, eResource := range scResponse.ExternalResource {
		if eResource.Kind == "ConfigMap" && eResource.Name == "rook-ceph-mon-endpoints" {
			monitorIps, err := csi.ExtractMonitor(eResource.Data)
			if err != nil {
				klog.Exitf("Failed to extract monitor data for storageClient %v: %v", consumerID, err)
			}
			csiClusterConfigEntry.Monitors = append(csiClusterConfigEntry.Monitors, monitorIps...)
		}
	}
	cc := csi.ClusterConfig{
		Client:    cl,
		Namespace: operatorNamespace,
		Ctx:       ctx,
	}
	err = cc.UpdateMonConfigMap("", consumerID, csiClusterConfigEntry)
	if err != nil {
		klog.Exitf("Failed to update mon configmap for storageClient %v: %v", consumerID, err)
	}
}
