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

package alert

import (
	"sort"

	"github.com/prometheus/client_golang/prometheus"
)

var _ prometheus.Collector = &Collector{}

// Collector exposes alerts fetched from the storage provider as
// Prometheus metrics. Each firing provider-side alert becomes a gauge:
//
//	ocs_client_alert{alert_name="X", severity="Y", storage_client="Z", ...} = value
//
// The "alertname" key from the provider labels is renamed to "alert_name" to
// avoid conflicting with Prometheus' reserved alertname label.
//
// Describe sends no descriptors so the collector is registered as "unchecked",
// which allows dynamic label sets per alert.
type Collector struct {
	runnable *Runnable
}

// NewCollector creates a collector that reads from ca's in-memory alert cache.
func NewCollector(ca *Runnable) *Collector {
	return &Collector{runnable: ca}
}

// Describe implements prometheus.Collector.
// Empty on purpose -- signals an unchecked collector to the registry because
// label names vary per alert.
func (c *Collector) Describe(chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	allAlerts := c.runnable.GetAllAlerts()
	for storageClientName, alerts := range allAlerts {
		for _, alert := range alerts {
			labelNames, labelValues := buildLabels(alert.AlertName, alert.Labels, storageClientName)

			desc := prometheus.NewDesc(
				"ocs_client_alert",
				"Alert relayed from the storage provider",
				labelNames, nil,
			)

			value := alert.Value
			if value == 0 {
				value = 1
			}

			m, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, value, labelValues...)
			if err != nil {
				continue
			}
			ch <- m
		}
	}
}

// buildLabels constructs sorted label name/value slices from the alert.
// "alertname" is renamed to "alert_name" (Prometheus reserves "alertname"),
// and "storage_client" is injected.
func buildLabels(alertName string, labels map[string]string, storageClient string) ([]string, []string) {
	keys := make([]string, 0, len(labels)+2)
	for k := range labels {
		if k == "alertname" {
			continue // replaced by alert_name below
		}
		keys = append(keys, k)
	}
	keys = append(keys, "alert_name", "storage_client")
	sort.Strings(keys)

	values := make([]string, len(keys))
	for i, k := range keys {
		switch k {
		case "alert_name":
			values[i] = alertName
		case "storage_client":
			values[i] = storageClient
		default:
			values[i] = labels[k]
		}
	}

	return keys, values
}
