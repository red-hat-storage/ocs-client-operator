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

package utils

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/red-hat-storage/ocs-operator/services/provider/api/v4/interfaces"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// OperatorNamespaceEnvVar is the constant for env variable OPERATOR_NAMESPACE
	// which is the namespace where operator pod is deployed.
	OperatorNamespaceEnvVar = "OPERATOR_NAMESPACE"

	// WatchNamespaceEnvVar is the constant for env variable WATCH_NAMESPACE
	// which indicates any other namespace to watch for resources.
	WatchNamespaceEnvVar = "WATCH_NAMESPACE"

	// OperatorPodNameEnvVar is the constant for env variable OPERATOR_POD_NAME
	OperatorPodNameEnvVar = "OPERATOR_POD_NAME"

	// StorageClientNameEnvVar is the constant for env variable STORAGE_CLIENT_NAME
	StorageClientNameEnvVar = "STORAGE_CLIENT_NAME"

	StatusReporterImageEnvVar = "STATUS_REPORTER_IMAGE"

	// Value corresponding to annotation key has subscription channel
	DesiredSubscriptionChannelAnnotationKey = "ocs.openshift.io/subscription.channel"

	// Value corresponding to annotation key has desired client hash
	DesiredConfigHashAnnotationKey = "ocs.openshift.io/provider-side-state"

	TopologyDomainLabelsAnnotationKey = "ocs.openshift.io/csi-rbd-topology-domain-labels"

	CronScheduleWeekly = "@weekly"

	ExitCodeThatShouldRestartTheProcess = 42

	OcsClientTimeout = 10 * time.Second

	OperatorVersionEnvVar = "OPERATOR_VERSION"
)

// GetOperatorNamespace returns the namespace where the operator is deployed.
func GetOperatorNamespace() string {
	return os.Getenv(OperatorNamespaceEnvVar)
}

func GetWatchNamespace() string {
	return os.Getenv(WatchNamespaceEnvVar)
}

func GetOperatorPodName() (string, error) {
	podName := os.Getenv(OperatorPodNameEnvVar)
	if podName == "" {
		return "", fmt.Errorf("OPERATOR_POD_NAME env doesn't contain pod name")
	}
	return podName, nil
}

func ValidateOperatorNamespace() error {
	ns := GetOperatorNamespace()
	if ns == "" {
		return fmt.Errorf("namespace not found for operator")
	}

	return nil
}

func ValidateStausReporterImage() error {
	image := os.Getenv(StatusReporterImageEnvVar)
	if image == "" {
		return fmt.Errorf("status reporter image not found")
	}

	return nil
}

// AddLabels adds values from newLabels to the keys on the supplied obj or overwrites values for existing keys on the obj
func AddLabels(obj metav1.Object, newLabels map[string]string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}
	maps.Copy(labels, newLabels)
}

// AddAnnotation adds label to a resource metadata, returns true if added else false
func AddLabel(obj metav1.Object, key string, value string) bool {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}
	if oldValue, exist := labels[key]; !exist || oldValue != value {
		labels[key] = value
		return true
	}
	return false
}

func AddAnnotations(obj metav1.Object, newAnnotations map[string]string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
		obj.SetAnnotations(annotations)
	}
	maps.Copy(annotations, newAnnotations)
}

// AddAnnotation adds an annotation to a resource metadata, returns true if added else false
func AddAnnotation(obj metav1.Object, key string, value string) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
		obj.SetAnnotations(annotations)
	}
	if oldValue, exist := annotations[key]; !exist || oldValue != value {
		annotations[key] = value
		return true
	}
	return false
}

func RemoveAnnotation(obj metav1.Object, key string) bool {
	annotations := obj.GetAnnotations()
	annotationCount := len(annotations)
	delete(annotations, key)
	return len(annotations) < annotationCount
}

func GetMD5Hash(text string) string {
	hash := md5.Sum([]byte(text))
	return hex.EncodeToString(hash[:])
}

func GetClusterResourceQuotaName(name string) string {
	return fmt.Sprintf("storage-client-%s-resourceqouta", GetMD5Hash(name))
}

func IsForbiddenError(err error) bool {
	statusErr, ok := err.(*errors.StatusError)
	if ok {
		for i := range statusErr.ErrStatus.Details.Causes {
			if statusErr.ErrStatus.Details.Causes[i].Type == metav1.CauseTypeForbidden {
				return true
			}
		}
	}
	return false
}

func SetClusterInformation(
	ctx context.Context,
	kubeClient client.Client,
	status interfaces.StorageClientInfo,
) error {

	clusterVersion := &configv1.ClusterVersion{}
	clusterVersion.Name = "version"
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(clusterVersion), clusterVersion); err != nil {
		return fmt.Errorf("failed to get cluster version: %v", err)
	}
	status.SetClusterID(string(clusterVersion.UID))

	historyRecord := Find(clusterVersion.Status.History, func(record *configv1.UpdateHistory) bool {
		return record.State == configv1.CompletedUpdate
	})
	if historyRecord == nil {
		return fmt.Errorf("unable to find the updated cluster version")
	}
	status.SetClientPlatformVersion(historyRecord.Version)

	clusterDNS := &configv1.DNS{}
	clusterDNS.Name = "cluster"
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(clusterDNS), clusterDNS); err != nil {
		return fmt.Errorf("failed to get clusterDNS %q: %v", clusterDNS.Name, err)
	}
	status.SetClusterName(clusterDNS.Spec.BaseDomain)

	return nil
}
