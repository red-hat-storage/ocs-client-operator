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

package clientalert

import (
	"context"

	snapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ prometheus.Collector = &ResourceCollector{}

// ResourceCollector exposes resource counts as Prometheus metrics.
// It reads from the controller-runtime cache (cached client) during each
// Prometheus scrape, so no background polling loop is needed.
type ResourceCollector struct {
	client client.Client
}

// NewResourceCollector creates a collector that counts CephFS PVs and
// ODF VolumeGroupSnapshotContent resources using the cached client.
func NewResourceCollector(c client.Client) *ResourceCollector {
	return &ResourceCollector{client: c}
}

// Describe implements prometheus.Collector.
// Empty on purpose -- unchecked collector.
func (c *ResourceCollector) Describe(chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector.
func (c *ResourceCollector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()

	c.collectCephFsPVCount(ctx, ch)
	c.collectVSCCount(ctx, ch)
}

func (c *ResourceCollector) collectCephFsPVCount(ctx context.Context, ch chan<- prometheus.Metric) {
	pvList := &corev1.PersistentVolumeList{}
	if err := c.client.List(ctx, pvList); err != nil {
		return
	}

	count := 0
	for i := range pvList.Items {
		pv := &pvList.Items[i]
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == templates.CephFsDriverName {
			count++
		}
	}

	desc := prometheus.NewDesc(
		"ocs_client_operator_cephfs_pv_count",
		"Number of CephFS PersistentVolumes on this client cluster",
		nil, nil,
	)
	m, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, float64(count))
	if err != nil {
		return
	}
	ch <- m
}

func (c *ResourceCollector) collectVSCCount(ctx context.Context, ch chan<- prometheus.Metric) {
	vscList := &snapapi.VolumeSnapshotContentList{}
	if err := c.client.List(ctx, vscList); err != nil {
		return
	}

	count := 0
	for i := range vscList.Items {
		if vscList.Items[i].Spec.Driver == templates.CephFsDriverName {
			count++
		}
	}

	desc := prometheus.NewDesc(
		"ocs_client_operator_cephfs_volume_snapshot_content_count",
		"Number of CephFS VolumeSnapshotContent resources on this client cluster",
		nil, nil,
	)
	m, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, float64(count))
	if err != nil {
		return
	}
	ch <- m
}
