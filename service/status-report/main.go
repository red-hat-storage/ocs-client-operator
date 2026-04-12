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
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	configv1 "github.com/openshift/api/config/v1"
	quotav1 "github.com/openshift/api/quota/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	providerclient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	"github.com/red-hat-storage/ocs-operator/services/provider/api/v4/interfaces"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
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

	metricsServiceName, isSet := os.LookupEnv(utils.MetricsServiceNameEnvVar)
	if !isSet {
		klog.Exitf("%s env var not set", utils.MetricsServiceNameEnvVar)
	}

	metricsPortStr, isSet := os.LookupEnv(utils.MetricsPortEnvVar)
	if !isSet || metricsPortStr == "" {
		klog.Exitf("%s env var not set", utils.MetricsPortEnvVar)
	}
	metricsPort, err := strconv.Atoi(metricsPortStr)
	if err != nil {
		klog.Exitf("Failed to parse %s: %v", utils.MetricsPortEnvVar, err)
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

	status := providerclient.NewStorageClientStatus()
	status.SetClientOperatorVersion(operatorVersion)
	status.SetClientOperatorNamespace(operatorNamespace)
	status.SetClientName(storageClientName)
	status.SetClientID(string(storageClient.UID))
	setStorageQuotaUtilizationRatio(ctx, cl, status)
	metricsEndpoint := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/metrics", metricsServiceName, operatorNamespace, metricsPort)
	setCephFsMetrics(status, metricsEndpoint, storageClientName)

	if err := utils.SetClusterInformation(ctx, cl, status); err != nil {
		klog.Warningf("Failed to set cluster information: %v", err)
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

const (
	cephFsPVCountMetric   = "ocs_client_operator_cephfs_pv_count"
	cephFsVSCCountMetric = "ocs_client_operator_cephfs_volume_snapshot_content_count"
)

func setCephFsMetrics(status interfaces.StorageClientStatus, metricsEndpoint, storageClientName string) {
	metricFamilies, err := fetchMetrics(metricsEndpoint)
	if err != nil {
		klog.Warningf("Failed to fetch metrics: %v", err)
		return
	}

	if count, err := findMetricValue(metricFamilies, cephFsPVCountMetric, storageClientName); err != nil {
		klog.Warningf("Failed to get CephFS PV count from metrics: %v", err)
	} else {
		status.SetCephFsPVCount(uint32(count))
	}

	if count, err := findMetricValue(metricFamilies, cephFsVSCCountMetric, storageClientName); err != nil {
		klog.Warningf("Failed to get CephFS VolumeSnapshotContent count from metrics: %v", err)
	} else {
		status.SetCephFsVolumeSnapshotContentCount(uint32(count))
	}
}

func fetchMetrics(endpoint string) (map[string]*dto.MetricFamily, error) {
	resp, err := http.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics from %s: %v", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("metrics endpoint returned status %d", resp.StatusCode)
	}

	parser := expfmt.NewTextParser(model.UTF8Validation)
	return parser.TextToMetricFamilies(resp.Body)
}

func findMetricValue(metricFamilies map[string]*dto.MetricFamily, metricName, storageClientName string) (float64, error) {
	mf, ok := metricFamilies[metricName]
	if !ok {
		return 0, fmt.Errorf("metric %q not found", metricName)
	}

	for _, m := range mf.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "storage_client" && lp.GetValue() == storageClientName {
				if gauge := m.GetGauge(); gauge != nil {
					return gauge.GetValue(), nil
				}
				return 0, fmt.Errorf("metric %q with storage_client=%q is not a gauge", metricName, storageClientName)
			}
		}
	}

	return 0, fmt.Errorf("metric %q with storage_client=%q not found", metricName, storageClientName)
}
