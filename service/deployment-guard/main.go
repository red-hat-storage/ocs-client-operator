package main

import (
	"context"
	"os"
	"time"

	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	// validations
	operatorNamespace := os.Getenv(utils.OperatorNamespaceEnvVar)
	if operatorNamespace == "" {
		klog.Exitf("%s env var is empty", utils.OperatorNamespaceEnvVar)
	}

	// creation of kube client
	scheme := runtime.NewScheme()
	cfg, err := config.GetConfig()
	if err != nil {
		klog.Exitf("Failed to get config: %v", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		klog.Exitf("Failed to create controller runtime client: %v", err)
	}
	ctx := context.Background()

	// NOTE: when we are running alongside odf-operator the CRDs corresponding
	// to an operator is installed first however it isn't guaranteed that CRDs
	// of all dependent operators are installed first before deploying any CSV.
	//
	// Due to above, StorageCluster CRD is sometimes created after we check for
	// it's existence and 1 min is the delay observed when client-op is listed
	// first and ocs-op listed last in the install plan for deploying CSVs.
	//
	// Please note this should be temporary upto when we develop new operator
	// which directly installs other operators based on runtime requirements
	// outside of OLM.
	klog.Info("Waiting for 90 sec before checking to allow operator to run")
	time.Sleep(90 * time.Second)

	// delay exponentially from half a sec and cap at 2 minutes
	delayFunc := wait.Backoff{
		Duration: 500 * time.Millisecond,
		Factor:   2,
		Jitter:   0.1,
		Steps:    10,
		Cap:      2 * time.Minute,
	}.DelayFunc()

	for !allowOperatorToRun(ctx, cl, operatorNamespace) {
		time.Sleep(delayFunc())
	}

}

func allowOperatorToRun(ctx context.Context, cl client.Client, namespace string) bool {
	// verify presence of StorageCluster CRD
	storageClusterCRD := &metav1.PartialObjectMetadata{}
	storageClusterCRD.SetGroupVersionKind(
		extv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"),
	)
	storageClusterCRD.Name = "storageclusters.ocs.openshift.io"
	if err := cl.Get(ctx, client.ObjectKeyFromObject(storageClusterCRD), storageClusterCRD); client.IgnoreNotFound(err) != nil {
		klog.Warning("Failed to find presence of StorageCluster CRD")
		return false
	}

	if storageClusterCRD.UID != "" {
		// StorageCluster CRD exists, wait till StorageCluster CR is configured in Provider mode
		storageClusters := &metav1.PartialObjectMetadataList{}
		storageClusters.SetGroupVersionKind(
			schema.GroupVersionKind{
				Group:   "ocs.openshift.io",
				Version: "v1",
				Kind:    "StorageCluster",
			},
		)
		if err := cl.List(ctx, storageClusters, client.InNamespace(namespace), client.Limit(1)); err != nil {
			klog.Warning("Failed to list StorageCluster CR")
			return false
		}
		if len(storageClusters.Items) < 1 {
			klog.Info("StorageCluster CR does not exist")
			return false
		}
		klog.Info("Waiting on deployment mode verification, no user action needed.")
		if storageClusters.Items[0].GetAnnotations()["ocs.openshift.io/deployment-mode"] != "provider" {
			return false
		}
	}

	klog.Info("Condition met to allow operator to run")
	return true
}
