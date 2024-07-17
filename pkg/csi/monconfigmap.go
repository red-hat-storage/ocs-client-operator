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
package csi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	// configMutex is used to prevent the config map from being updated
	// for multiple clusters simultaneously.
	configMutex = &sync.Mutex{}
)

type ClusterConfigEntry struct {
	ClusterID       string       `json:"clusterID"`
	StorageClientID string       `json:"storageClientID"`
	Monitors        []string     `json:"monitors"`
	CephFS          *CephFSSpec  `json:"cephFS,omitempty"`
	CephRBD         *CephRBDSpec `json:"rbd,omitempty"`
}

type CephRBDSpec struct {
	RadosNamespace string `json:"radosNamespace,omitempty"`
}

type CephFSSpec struct {
	SubvolumeGroup     string `json:"subvolumeGroup,omitempty"`
	KernelMountOptions string `json:"kernelMountOptions,omitempty"`
}

type ClusterConfig struct {
	client.Client
	Ctx       context.Context
	Namespace string
}

type csiClusterConfig []ClusterConfigEntry

func parseCsiClusterConfig(c string) (csiClusterConfig, error) {
	var cc csiClusterConfig
	err := json.Unmarshal([]byte(c), &cc)
	if err != nil {
		return cc, errors.Wrap(err, "failed to parse csi cluster config")
	}
	return cc, nil
}

func formatCsiClusterConfig(cc csiClusterConfig) (string, error) {
	ccJSON, err := json.Marshal(cc)
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal csi cluster config")
	}
	return string(ccJSON), nil
}

// updateCsiClusterConfig returns a json-formatted string containing
// the cluster-to-mon mapping required to configure ceph csi.
func updateCsiClusterConfig(curr, clusterKey, storageClientID string, newClusterConfigEntry *ClusterConfigEntry) (string, error) {
	var (
		cc     csiClusterConfig
		centry ClusterConfigEntry
		found  bool
	)

	cc, err := parseCsiClusterConfig(curr)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse current csi cluster config")
	}

	// Regardless of which controllers call updateCsiClusterConfig(), the values will be preserved since
	// a lock is acquired for the update operation. So concurrent updates (rare event) will block and
	// wait for the other update to complete. Monitors and Subvolumegroup will be updated
	// independently and won't collide.
	if newClusterConfigEntry != nil {
		for i, centry := range cc {
			// If the clusterID belongs to the same cluster, update the entry.
			if storageClientID == cc[i].StorageClientID || clusterKey == newClusterConfigEntry.ClusterID {
				centry.Monitors = newClusterConfigEntry.Monitors
				centry.StorageClientID = storageClientID
				cc[i] = centry
			}
		}
	}
	for i, centry := range cc {
		if centry.ClusterID == clusterKey {
			// If the new entry is nil, this means the entry is being deleted so remove it from the list
			if newClusterConfigEntry == nil {
				cc = append(cc[:i], cc[i+1:]...)
				found = true
				break
			}
			centry.StorageClientID = storageClientID
			centry.Monitors = newClusterConfigEntry.Monitors
			if newClusterConfigEntry.CephFS != nil && (newClusterConfigEntry.CephFS.SubvolumeGroup != "") {
				centry.CephFS = newClusterConfigEntry.CephFS
			}
			if newClusterConfigEntry.CephRBD != nil && (newClusterConfigEntry.CephRBD.RadosNamespace != "") {
				centry.CephRBD = newClusterConfigEntry.CephRBD
			}
			found = true
			cc[i] = centry
			break
		}
	}
	if !found {
		// If it's the first time we create the cluster, the entry does not exist, so the removal
		// will fail with a dangling pointer
		if newClusterConfigEntry != nil && clusterKey != "" {
			centry.ClusterID = clusterKey
			centry.StorageClientID = storageClientID
			centry.Monitors = newClusterConfigEntry.Monitors
			// Add a condition not to fill with empty values
			if newClusterConfigEntry.CephFS != nil && (newClusterConfigEntry.CephFS.SubvolumeGroup != "") {
				centry.CephFS = newClusterConfigEntry.CephFS
			}
			if newClusterConfigEntry.CephRBD != nil && (newClusterConfigEntry.CephRBD.RadosNamespace != "") {
				centry.CephRBD = newClusterConfigEntry.CephRBD
			}
			cc = append(cc, centry)
		}
	}

	return formatCsiClusterConfig(cc)
}

// UpdateMonConfigMap updates the config map used to provide ceph-csi with
// basic cluster configuration. The clusterID and storageClientID are
// used to determine what "cluster" in the config map will be updated and
// the clusterID value is expected to match the clusterID
// value that is provided to ceph-csi uses in the storage class.
// The locker configMutex is typically a mutex and is used to prevent the config
// map from being updated for multiple clusters simultaneously.
func (c *ClusterConfig) UpdateMonConfigMap(clusterID, storageClientID string, newClusterConfigEntry *ClusterConfigEntry) error {
	ConfigKey := "config.json"
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templates.MonConfigMapName,
			Namespace: c.Namespace,
		},
		Data: map[string]string{
			ConfigKey: "[]",
		},
	}

	configMutex.Lock()
	defer configMutex.Unlock()

	// fetch current ConfigMap contents
	err := c.Get(c.Ctx, types.NamespacedName{Name: configMap.Name, Namespace: configMap.Namespace}, configMap)
	if err != nil {
		return errors.Wrap(err, "failed to fetch current csi config map")
	}

	// update ConfigMap contents for current cluster
	currData := configMap.Data[ConfigKey]
	newData, err := updateCsiClusterConfig(currData, clusterID, storageClientID, newClusterConfigEntry)
	if err != nil {
		return errors.Wrap(err, "failed to update csi config map data")
	}
	configMap.Data[ConfigKey] = newData

	err = c.Update(c.Ctx, configMap)
	if err != nil {
		return errors.Wrapf(err, "failed to update monitor configmap %q", configMap.Name)
	}

	return nil
}

func ExtractMonitor(monitorData []byte) ([]string, error) {
	data := map[string]string{}
	monitorIPs := []string{}
	err := json.Unmarshal(monitorData, &data)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal data: %v", err)
	}
	// Ip will be in the format of "b=172.30.60.238:6789","c=172.30.162.124:6789","a=172.30.1.100:6789"
	monIPs := strings.Split(data["data"], ",")
	for _, monIP := range monIPs {
		ip := strings.Split(monIP, "=")
		if len(ip) != 2 {
			return nil, fmt.Errorf("invalid mon ips: %s", monIPs)
		}
		monitorIPs = append(monitorIPs, ip[1])
	}
	return monitorIPs, nil
}
