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
	"context"
	"strings"

	csiopv1 "github.com/ceph/ceph-csi-operator/api/v1"
	snapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These must match the index names registered in
// internal/controller/storageclient_controller.go SetupWithManager.
const (
	ownerUIDIndexName     = "index:ownerUID"
	pvClusterIDIndexName  = "index:persistentVolumeClusterID"
	vscClusterIDIndexName = "index:volumeSnapshotContentCSIDriver"
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

	storageClients := &v1alpha1.StorageClientList{}
	if err := c.client.List(ctx, storageClients); err != nil {
		return
	}

	c.collectPVCount(ctx, ch, storageClients, templates.CephFsDriverName)
	c.collectVSCCount(ctx, ch, storageClients, templates.CephFsDriverName)
}

func (c *ResourceCollector) clientProfileName(ctx context.Context, sc *v1alpha1.StorageClient) (string, error) {
	profileList := &csiopv1.ClientProfileList{}
	if err := c.client.List(ctx, profileList, client.MatchingFields{ownerUIDIndexName: string(sc.UID)}); err != nil {
		return "", err
	}
	if len(profileList.Items) == 0 {
		return "", nil
	}
	return profileList.Items[0].Name, nil
}

func (c *ResourceCollector) collectPVCount(ctx context.Context, ch chan<- prometheus.Metric, storageClients *v1alpha1.StorageClientList, driverName string) {
	parts := strings.Split(driverName, ".")
	// parts = ["openshift-storage", "cephfs", "csi", "ceph", "com"]
	driverToken := parts[1]

	desc := prometheus.NewDesc(
		"ocs_client_operator_"+driverToken+"_pv_count",
		"Number of "+driverToken+" PersistentVolumes on this client cluster",
		[]string{"storage_client"}, nil,
	)

	for i := range storageClients.Items {
		sc := &storageClients.Items[i]
		profileName, err := c.clientProfileName(ctx, sc)
		if err != nil || profileName == "" {
			continue
		}

		pvList := &corev1.PersistentVolumeList{}
		if err := c.client.List(ctx, pvList, client.MatchingFields{pvClusterIDIndexName: profileName}); err != nil {
			continue
		}

		count := 0
		for j := range pvList.Items {
			if pvList.Items[j].Spec.CSI != nil && pvList.Items[j].Spec.CSI.Driver == driverName {
				count++
			}
		}

		m, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, float64(count), sc.Name)
		if err != nil {
			continue
		}
		ch <- m
	}
}

func (c *ResourceCollector) collectVSCCount(ctx context.Context, ch chan<- prometheus.Metric, storageClients *v1alpha1.StorageClientList, driverName string) {
	desc := prometheus.NewDesc(
		"ocs_client_operator_cephfs_volume_snapshot_content_count",
		"Number of CephFS VolumeSnapshotContent resources on this client cluster",
		[]string{"storage_client"}, nil,
	)

	for i := range storageClients.Items {
		sc := &storageClients.Items[i]
		profileName, err := c.clientProfileName(ctx, sc)
		if err != nil || profileName == "" {
			continue
		}

		vscList := &snapapi.VolumeSnapshotContentList{}
		if err := c.client.List(ctx, vscList, client.MatchingFields{vscClusterIDIndexName: profileName}); err != nil {
			continue
		}

		count := 0
		for j := range vscList.Items {
			if vscList.Items[j].Spec.Driver == driverName {
				count++
			}
		}

		m, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, float64(count), sc.Name)
		if err != nil {
			continue
		}
		ch <- m
	}
}
