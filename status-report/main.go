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
	"time"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	providerclient "github.com/red-hat-storage/ocs-operator/services/provider/client"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		klog.Exitf("Failed to add v1alpha1 to scheme: %v", err)
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

	storageclient := &v1alpha1.StorageClient{}
	storageclient.Name = storageClientName
	storageclient.Namespace = storageClientNamespace

	if err = cl.Get(ctx, types.NamespacedName{Name: storageclient.Name, Namespace: storageclient.Namespace}, storageclient); err != nil {
		klog.Exitf("Failed to get storageClient %q/%q: %v", storageclient.Namespace, storageclient.Name, err)
	}

	providerClient, err := providerclient.NewProviderClient(
		ctx,
		storageclient.Spec.StorageProviderEndpoint,
		10*time.Second,
	)
	if err != nil {
		klog.Exitf("Failed to create grpc client: %v", err)
	}
	defer providerClient.Close()

	if _, err = providerClient.ReportStatus(ctx, storageclient.Status.ConsumerID); err != nil {
		klog.Exitf("Failed to update lastHeartbeat of storageClient %v: %v", storageclient.Status.ConsumerID, err)
	}

}
