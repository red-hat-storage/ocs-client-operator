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
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	pb "github.com/red-hat-storage/ocs-operator/services/provider/api/v4"
	providerClient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	OperatorConfigMapName = "ocs-client-operator-config"
	DefaultPollInterval   = 1 * time.Minute
)

// Runnable is a manager.Runnable that periodically fetches firing alerts
// from the storage provider for each connected StorageClient.
type Runnable struct {
	client.Client
	namespace    string
	logger       logr.Logger
	pollInterval atomic.Int64

	mu         sync.RWMutex
	lastAlerts map[string][]*pb.AlertInfo
}

// NewRunnable creates a new alert Runnable.
func NewRunnable(c client.Client, namespace string, logger logr.Logger, pollInterval time.Duration) *Runnable {
	r := &Runnable{
		Client:     c,
		namespace:  namespace,
		logger:     logger,
		lastAlerts: make(map[string][]*pb.AlertInfo),
	}
	r.pollInterval.Store(int64(pollInterval))
	return r
}

// SetPollInterval atomically updates the poll interval.
func (ca *Runnable) SetPollInterval(d time.Duration) {
	ca.pollInterval.Store(int64(d))
}

// GetPollInterval atomically reads the poll interval.
func (ca *Runnable) GetPollInterval() time.Duration {
	return time.Duration(ca.pollInterval.Load())
}

// GetAllAlerts returns all currently cached alerts keyed by StorageClient name.
func (ca *Runnable) GetAllAlerts() map[string][]*pb.AlertInfo {
	ca.mu.RLock()
	defer ca.mu.RUnlock()
	copied := make(map[string][]*pb.AlertInfo, len(ca.lastAlerts))
	for key, alerts := range ca.lastAlerts {
		newAlerts := make([]*pb.AlertInfo, len(alerts))
		copy(newAlerts, alerts)
		copied[key] = newAlerts
	}
	return copied
}

// Start implements manager.Runnable. It periodically polls for client alerts.
func (ca *Runnable) Start(ctx context.Context) error {
	currentInterval := ca.GetPollInterval()
	ca.logger.V(5).Info("starting client alert runnable", "pollInterval", currentInterval)

	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			ca.logger.V(5).Info("stopping client alert runnable")
			return nil
		case <-ticker.C:
			if newInterval := ca.GetPollInterval(); newInterval != currentInterval {
				ca.logger.V(5).Info("updating poll interval", "pollInterval", newInterval)
				ticker.Reset(newInterval)
				currentInterval = newInterval
			}
			ca.fetchAlerts(ctx)
		}
	}
}

// fetchAlerts fetches alerts for all connected StorageClients.
func (ca *Runnable) fetchAlerts(ctx context.Context) {
	storageClientList := &v1alpha1.StorageClientList{}
	if err := ca.List(ctx, storageClientList); err != nil {
		ca.logger.Error(err, "failed to list StorageClients")
		return
	}

	newAlerts := make(map[string][]*pb.AlertInfo, len(storageClientList.Items))

	for i := range storageClientList.Items {
		sc := &storageClientList.Items[i]

		if sc.Status.Phase != v1alpha1.StorageClientConnected {
			ca.logger.V(5).Info("skipping StorageClient not in Connected phase", "storageClient", sc.Name, "phase", sc.Status.Phase)
			continue
		}

		if sc.Status.ConsumerID == "" {
			ca.logger.V(5).Info("skipping StorageClient with empty consumerID", "storageClient", sc.Name)
			continue
		}

		alerts, err := ca.fetchAlertsForClient(ctx, sc)
		if err != nil {
			ca.logger.Error(err, "failed to fetch alerts for StorageClient", "storageClient", sc.Name)
			continue
		}

		newAlerts[sc.Name] = alerts
		ca.logger.V(5).Info("fetched alerts", "storageClient", sc.Name, "alertCount", len(alerts))

		for _, alert := range alerts {
			ca.logger.V(5).Info("alert detail",
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
func (ca *Runnable) fetchAlertsForClient(ctx context.Context, sc *v1alpha1.StorageClient) ([]*pb.AlertInfo, error) {
	var serverCA []byte
	var clientCert []byte
	var clientKey []byte

	if sc.Spec.ServerCASecret != nil && sc.Spec.ServerCASecret.Name != "" {
		secret := &corev1.Secret{}
		err := ca.Get(ctx, types.NamespacedName{
			Name:      sc.Spec.ServerCASecret.Name,
			Namespace: ca.namespace,
		}, secret)
		if err != nil {
			return nil, fmt.Errorf("failed to get server CA secret %s: %v", sc.Spec.ServerCASecret.Name, err)
		}
		serverCA = secret.Data["ca.crt"]
		if len(serverCA) == 0 {
			return nil, fmt.Errorf("ca.crt not found in server CA secret %s", sc.Spec.ServerCASecret.Name)
		}
	}

	if sc.Spec.ClientCertSecret != nil && sc.Spec.ClientCertSecret.Name != "" {
		secret := &corev1.Secret{}
		err := ca.Get(ctx, types.NamespacedName{
			Name:      sc.Spec.ClientCertSecret.Name,
			Namespace: ca.namespace,
		}, secret)
		if err != nil {
			return nil, fmt.Errorf("failed to get client cert secret %s: %v", sc.Spec.ClientCertSecret.Name, err)
		}
		clientCert = secret.Data["tls.crt"]
		clientKey = secret.Data["tls.key"]
		if len(clientCert) == 0 || len(clientKey) == 0 {
			return nil, fmt.Errorf("tls.crt or tls.key not found in client cert secret %s", sc.Spec.ClientCertSecret.Name)
		}
	}

	ocsProviderClient, err := providerClient.NewProviderClient(
		ctx,
		sc.Spec.StorageProviderEndpoint,
		utils.OcsClientTimeout,
		serverCA,
		sc.Spec.ServerName,
		clientCert,
		clientKey,
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
