/*
Copyright 2026 Red Hat, Inc.

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

package alert

import (
	"strings"
	"testing"

	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testNamespace = "openshift-storage"

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	err := v1alpha1.AddToScheme(scheme)
	assert.NoError(t, err)
	err = opv1a1.AddToScheme(scheme)
	assert.NoError(t, err)
	return scheme
}

func newSubscription(name, namespace, channel string) *opv1a1.Subscription {
	return &opv1a1.Subscription{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: &opv1a1.SubscriptionSpec{
			Package: name,
			Channel: channel,
		},
	}
}

func newStorageClient(name string, annotations map[string]string) *v1alpha1.StorageClient {
	return &v1alpha1.StorageClient{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
	}
}

// collectUpgradeMetrics collects only the upgrade_required metrics from the collector
func collectUpgradeMetrics(t *testing.T, collector prometheus.Collector) []*dto.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 10)
	go func() {
		collector.Collect(ch)
		close(ch)
	}()

	var metrics []*dto.Metric
	for m := range ch {
		// Filter for upgrade_required metrics by checking the description
		desc := m.Desc().String()
		if !strings.Contains(desc, "ocs_client_operator_upgrade_required") {
			continue
		}
		metric := &dto.Metric{}
		err := m.Write(metric)
		assert.NoError(t, err)
		metrics = append(metrics, metric)
	}
	return metrics
}

func getLabelValue(metric *dto.Metric, labelName string) string {
	for _, label := range metric.Label {
		if label.GetName() == labelName {
			return label.GetValue()
		}
	}
	return ""
}

func TestResourceCollector_UpgradeRequired_MatchingChannels_NoMetric(t *testing.T) {
	scheme := newTestScheme(t)

	subscription := newSubscription(ocsClientOperatorPackageName, testNamespace, "stable-4.18")
	storageClient := newStorageClient("test-client", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "stable-4.18",
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(subscription, storageClient).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Empty(t, metrics, "Expected no metrics when channels match")
}

func TestResourceCollector_UpgradeRequired_MismatchedChannels_MetricEmitted(t *testing.T) {
	scheme := newTestScheme(t)

	subscription := newSubscription(ocsClientOperatorPackageName, testNamespace, "stable-4.18")
	storageClient := newStorageClient("test-client", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "stable-4.19",
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(subscription, storageClient).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Len(t, metrics, 1, "Expected one metric when channels mismatch")

	metric := metrics[0]
	assert.Equal(t, float64(1), metric.Gauge.GetValue())
	assert.Equal(t, "test-client", getLabelValue(metric, "storage_client"))
	assert.Equal(t, "stable-4.19", getLabelValue(metric, "desired_channel"))
	assert.Equal(t, "stable-4.18", getLabelValue(metric, "current_channel"))
}

func TestResourceCollector_UpgradeRequired_NoAnnotation_NoMetric(t *testing.T) {
	scheme := newTestScheme(t)

	subscription := newSubscription(ocsClientOperatorPackageName, testNamespace, "stable-4.18")
	storageClient := newStorageClient("test-client", nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(subscription, storageClient).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Empty(t, metrics, "Expected no metrics when annotation is missing")
}

func TestResourceCollector_UpgradeRequired_EmptyAnnotation_NoMetric(t *testing.T) {
	scheme := newTestScheme(t)

	subscription := newSubscription(ocsClientOperatorPackageName, testNamespace, "stable-4.18")
	storageClient := newStorageClient("test-client", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "",
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(subscription, storageClient).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Empty(t, metrics, "Expected no metrics when annotation is empty")
}

func TestResourceCollector_UpgradeRequired_NoSubscription_NoMetric(t *testing.T) {
	scheme := newTestScheme(t)

	storageClient := newStorageClient("test-client", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "stable-4.19",
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(storageClient).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Empty(t, metrics, "Expected no metrics when subscription doesn't exist")
}

func TestResourceCollector_UpgradeRequired_NoStorageClients_NoMetric(t *testing.T) {
	scheme := newTestScheme(t)

	subscription := newSubscription(ocsClientOperatorPackageName, testNamespace, "stable-4.18")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(subscription).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Empty(t, metrics, "Expected no metrics when no StorageClients exist")
}

func TestResourceCollector_UpgradeRequired_MultipleStorageClients_OneMismatch(t *testing.T) {
	scheme := newTestScheme(t)

	subscription := newSubscription(ocsClientOperatorPackageName, testNamespace, "stable-4.18")
	storageClientA := newStorageClient("client-a", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "stable-4.19", // mismatch
	})
	storageClientB := newStorageClient("client-b", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "stable-4.18", // match
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(subscription, storageClientA, storageClientB).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Len(t, metrics, 1, "Expected one metric for the mismatched client only")
	assert.Equal(t, "client-a", getLabelValue(metrics[0], "storage_client"))
}

func TestResourceCollector_UpgradeRequired_MultipleStorageClients_BothMismatch(t *testing.T) {
	scheme := newTestScheme(t)

	subscription := newSubscription(ocsClientOperatorPackageName, testNamespace, "stable-4.18")
	storageClientA := newStorageClient("client-a", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "stable-4.19",
	})
	storageClientB := newStorageClient("client-b", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "stable-4.20",
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(subscription, storageClientA, storageClientB).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Len(t, metrics, 2, "Expected two metrics for both mismatched clients")

	// Check that both clients are represented
	clientNames := make(map[string]bool)
	for _, m := range metrics {
		clientNames[getLabelValue(m, "storage_client")] = true
	}
	assert.True(t, clientNames["client-a"], "Expected metric for client-a")
	assert.True(t, clientNames["client-b"], "Expected metric for client-b")
}

func TestResourceCollector_UpgradeRequired_OtherSubscription_Ignored(t *testing.T) {
	scheme := newTestScheme(t)

	// Subscription for a different operator
	otherSubscription := newSubscription("other-operator", testNamespace, "stable-4.18")
	storageClient := newStorageClient("test-client", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "stable-4.19",
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(otherSubscription, storageClient).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Empty(t, metrics, "Expected no metrics when only non-ocs-client-operator subscriptions exist")
}

func TestResourceCollector_UpgradeRequired_SubscriptionInDifferentNamespace_Ignored(t *testing.T) {
	scheme := newTestScheme(t)

	// Subscription in a different namespace
	subscription := newSubscription(ocsClientOperatorPackageName, "other-namespace", "stable-4.18")
	storageClient := newStorageClient("test-client", map[string]string{
		utils.DesiredSubscriptionChannelAnnotationKey: "stable-4.19",
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(subscription, storageClient).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)
	metrics := collectUpgradeMetrics(t, collector)

	assert.Empty(t, metrics, "Expected no metrics when subscription is in different namespace")
}

// Ensure the collector implements prometheus.Collector interface
func TestResourceCollector_ImplementsCollectorInterface(t *testing.T) {
	var _ prometheus.Collector = &ResourceCollector{}
}

// Ensure Describe returns nothing (unchecked collector)
func TestResourceCollector_DescribeReturnsNothing(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)

	ch := make(chan *prometheus.Desc, 10)
	collector.Describe(ch)
	close(ch)

	var descs []*prometheus.Desc
	for d := range ch {
		descs = append(descs, d)
	}

	assert.Empty(t, descs, "Describe should return no descriptors for unchecked collector")
}

// Helper to verify collector can be registered without panic
func TestResourceCollector_CanBeRegistered(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		Build()

	collector := NewResourceCollector(fakeClient, testNamespace)

	registry := prometheus.NewRegistry()
	err := registry.Register(collector)
	assert.NoError(t, err, "Collector should register without error")
}
