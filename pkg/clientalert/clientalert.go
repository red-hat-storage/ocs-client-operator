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
	"maps"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	pb "github.com/red-hat-storage/ocs-operator/services/provider/api/v4"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operatorConfigMapName = "ocs-client-operator-config"
	alertPollIntervalKey  = "alertPollInterval"
	defaultPollInterval   = 1 * time.Minute
)

// ClientAlert is a manager.Runnable that periodically fetches firing alerts
// from the storage provider for each connected StorageClient.
type ClientAlert struct {
	client.Client
	namespace string
	logger    logr.Logger

	mu         sync.RWMutex
	lastAlerts map[string][]*pb.AlertInfo
}

// NewClientAlert creates a new ClientAlert runnable.
func NewClientAlert(c client.Client, namespace string, logger logr.Logger) *ClientAlert {
	return &ClientAlert{
		Client:     c,
		namespace:  namespace,
		logger:     logger,
		lastAlerts: make(map[string][]*pb.AlertInfo),
	}
}

// GetLastAlerts returns the most recently fetched alerts for the given StorageClient.
func (ca *ClientAlert) GetLastAlerts(storageClientName string) []*pb.AlertInfo {
	ca.mu.RLock()
	defer ca.mu.RUnlock()
	return ca.lastAlerts[storageClientName]
}

// GetAllAlerts returns all currently cached alerts keyed by StorageClient name.
func (ca *ClientAlert) GetAllAlerts() map[string][]*pb.AlertInfo {
	ca.mu.RLock()
	defer ca.mu.RUnlock()
	copied := make(map[string][]*pb.AlertInfo, len(ca.lastAlerts))
	maps.Copy(copied, ca.lastAlerts)
	return copied
}

// Start implements manager.Runnable. It periodically polls for client alerts.
func (ca *ClientAlert) Start(ctx context.Context) error {
	interval := ca.getPollInterval(ctx)
	ca.logger.V(1).Info("starting client alert runnable", "pollInterval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			ca.logger.V(1).Info("stopping client alert runnable")
			return nil
		case <-ticker.C:
			ca.fetchAlerts(ctx)

			newInterval := ca.getPollInterval(ctx)
			if newInterval != interval {
				ca.logger.V(1).Info("poll interval changed", "old", interval, "new", newInterval)
				interval = newInterval
				ticker.Reset(interval)
			}
		}
	}
}

// getPollInterval reads the poll interval from the operator ConfigMap.
// Returns defaultPollInterval if the ConfigMap or key is not found or cannot be parsed.
func (ca *ClientAlert) getPollInterval(ctx context.Context) time.Duration {
	cm := &corev1.ConfigMap{}
	cm.Name = operatorConfigMapName
	cm.Namespace = ca.namespace

	if err := ca.Get(ctx, client.ObjectKeyFromObject(cm), cm); err != nil {
		ca.logger.V(1).Info("failed to get operator configmap, using default poll interval", "error", err)
		return defaultPollInterval
	}

	intervalStr, ok := cm.Data[alertPollIntervalKey]
	if !ok || intervalStr == "" {
		ca.logger.V(1).Info("alertPollInterval not set in configmap, using default", "default", defaultPollInterval)
		return defaultPollInterval
	}

	d, err := time.ParseDuration(intervalStr)
	if err != nil {
		ca.logger.Error(err, "failed to parse alertPollInterval, using default", "value", intervalStr, "default", defaultPollInterval)
		return defaultPollInterval
	}

	return d
}

// fetchAlerts fetches alerts for all connected StorageClients.
func (ca *ClientAlert) fetchAlerts(ctx context.Context) {
	storageClientList := &v1alpha1.StorageClientList{}
	if err := ca.List(ctx, storageClientList); err != nil {
		ca.logger.Error(err, "failed to list StorageClients")
		return
	}

	newAlerts := make(map[string][]*pb.AlertInfo, len(storageClientList.Items))

	for i := range storageClientList.Items {
		sc := &storageClientList.Items[i]

		if sc.Status.Phase != v1alpha1.StorageClientConnected {
			ca.logger.V(1).Info("skipping StorageClient not in Connected phase", "storageClient", sc.Name, "phase", sc.Status.Phase)
			continue
		}

		if sc.Status.ConsumerID == "" {
			ca.logger.V(1).Info("skipping StorageClient with empty consumerID", "storageClient", sc.Name)
			continue
		}

		alerts, err := ca.fetchAlertsForClient(ctx, sc)
		if err != nil {
			ca.logger.Error(err, "failed to fetch alerts for StorageClient", "storageClient", sc.Name)
			continue
		}

		newAlerts[sc.Name] = alerts
		ca.logger.V(1).Info("fetched alerts", "storageClient", sc.Name, "alertCount", len(alerts))

		for _, alert := range alerts {
			ca.logger.V(1).Info("alert detail",
				"storageClient", sc.Name,
				"alertName", alert.AlertName,
				"value", alert.Value,
				"labels", alert.Labels,
			)
		}
	}

	ca.mu.Lock()
	ca.lastAlerts = newAlerts
	ca.mu.Unlock()
}

// fetchAlertsForClient calls the GetClientAlerts gRPC RPC for a single StorageClient.
func (ca *ClientAlert) fetchAlertsForClient(ctx context.Context, sc *v1alpha1.StorageClient) ([]*pb.AlertInfo, error) {
	ocsProviderClient, err := providerClient.NewProviderClient(
		ctx, sc.Spec.StorageProviderEndpoint, utils.OcsClientTimeout,
	)
	if err != nil {
		return nil, err
	}
	defer ocsProviderClient.Close()

	resp, err := ocsProviderClient.GetClientAlerts(ctx, sc.Status.ConsumerID)
	if err != nil {
		return nil, err
	}

	return resp.Alerts, nil
}
