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
	stResponse, err := providerClient.ReportStatus(ctx, storageClient.Status.ConsumerID, status)
	if err != nil {
		klog.Exitf("Failed to report status of storageClient %v: %v", storageClient.Status.ConsumerID, err)
	}

	if storageClient.Spec.OperatorVersion != stResponse.ProviderOperatorVersion {
		storageClient.Spec.OperatorVersion = stResponse.ProviderOperatorVersion
		if err = cl.Update(ctx, storageClient); err != nil {
			klog.Warningf("Failed to update Storage Client: %v", err)
		}
	}

	var csiClusterConfigEntry = new(csi.ClusterConfigEntry)
	scResponse, err := providerClient.GetStorageConfig(ctx, storageClient.Status.ConsumerID)
	if err != nil {
		klog.Exitf("Failed to get StorageConfig of storageClient %v: %v", storageClient.Status.ConsumerID, err)
	}
	for _, eResource := range scResponse.ExternalResource {
		if eResource.Kind == "ConfigMap" && eResource.Name == "rook-ceph-mon-endpoints" {
			monitorIps, err := csi.ExtractMonitor(eResource.Data)
			if err != nil {
				klog.Exitf("Failed to extract monitor data for storageClient %v: %v", storageClient.Status.ConsumerID, err)
			}
			csiClusterConfigEntry.Monitors = append(csiClusterConfigEntry.Monitors, monitorIps...)
		}
	}
	cc := csi.ClusterConfig{
		Client:    cl,
		Namespace: operatorNamespace,
		Ctx:       ctx,
	}
	err = cc.UpdateMonConfigMap("", storageClient.Status.ConsumerID, csiClusterConfigEntry)
	if err != nil {
		klog.Exitf("Failed to update mon configmap for storageClient %v: %v", storageClient.Status.ConsumerID, err)
	}
}
