/*
Copyright 2024 Red Hat, Inc.

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

package controller

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	alertCollectorInterval = 60 * time.Second
)

// alertEntry holds a snapshot of one firing alert from the provider.
type alertEntry struct {
	alertName string
	labels    map[string]string
	value     float64
}

// AlertCollector periodically calls GetClientAlerts() for every
// StorageClient and re-publishes each alert as a Prometheus gauge.
// The metric is fully untyped on the collector side – all domain
// semantics are deferred to the PrometheusRule CR that matches on
// alertname/labels.
type AlertCollector struct {
	client client.Client

	mu     sync.Mutex
	alerts []alertEntry
	desc   *prometheus.Desc
}

// NewAlertCollector creates and registers the collector.
func NewAlertCollector(cl client.Client) *AlertCollector {
	c := &AlertCollector{
		client: cl,
		desc: prometheus.NewDesc(
			"ocs_client_provider_alert",
			"Alerts forwarded from the OCS storage provider cluster",
			nil,
			nil,
		),
	}
	metrics.Registry.MustRegister(c)
	return c
}

// Describe implements prometheus.Collector.
func (c *AlertCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect implements prometheus.Collector.
// It emits one gauge per cached alert, attaching every provider-side
// label so that the PrometheusRule on the client cluster can reference
// them freely.
func (c *AlertCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.alerts {
		a := &c.alerts[i]
		labelNames := make([]string, 0, len(a.labels))
		labelValues := make([]string, 0, len(a.labels))
		for k, v := range a.labels {
			labelNames = append(labelNames, k)
			labelValues = append(labelValues, v)
		}
		desc := prometheus.NewDesc(
			"ocs_client_provider_alert",
			"Alerts forwarded from the OCS storage provider cluster",
			labelNames,
			nil,
		)
		m, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, a.value, labelValues...)
		if err == nil {
			ch <- m
		}
	}
}

// Start implements manager.Runnable – it is added to the controller-runtime
// manager and runs until the context is cancelled.
func (c *AlertCollector) Start(ctx context.Context) error {
	log := ctrl.Log.WithName("alert-collector")
	log.Info("starting alert collector")

	ticker := time.NewTicker(alertCollectorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("stopping alert collector")
			return nil
		case <-ticker.C:
			c.poll(ctx, log)
		}
	}
}

func (c *AlertCollector) poll(ctx context.Context, log logr.Logger) {
	storageClients := &v1alpha1.StorageClientList{}
	if err := c.client.List(ctx, storageClients); err != nil {
		log.Error(err, "failed to list StorageClients")
		return
	}

	var collected []alertEntry

	for i := range storageClients.Items {
		sc := &storageClients.Items[i]
		if sc.Status.ConsumerID == "" || sc.Status.Phase != v1alpha1.StorageClientConnected {
			continue
		}

		provider, err := providerClient.NewProviderClient(ctx, sc.Spec.StorageProviderEndpoint, utils.OcsClientTimeout)
		if err != nil {
			log.Error(err, "failed to create provider client", "storageClient", sc.Name)
			continue
		}

		resp, err := provider.GetClientAlerts(ctx, sc.Status.ConsumerID)
		provider.Close()
		if err != nil {
			log.Error(err, "failed to get client alerts", "storageClient", sc.Name)
			continue
		}

		for _, alert := range resp.GetAlerts() {
			// Copy labels, renaming "alertname" to avoid conflict with
			// the PrometheusRule alert name, and ensure the alert name
			// from the proto field is always present.
			labels := make(map[string]string, len(alert.GetLabels())+1)
			for k, v := range alert.GetLabels() {
				if k == "alertname" {
					labels["provider_alert_name"] = v
				} else {
					labels[k] = v
				}
			}
			if _, ok := labels["provider_alert_name"]; !ok {
				labels["provider_alert_name"] = alert.GetAlertName()
			}
			collected = append(collected, alertEntry{
				alertName: alert.GetAlertName(),
				labels:    labels,
				value:     alert.GetValue(),
			})
		}
	}

	c.mu.Lock()
	c.alerts = collected
	c.mu.Unlock()
}
