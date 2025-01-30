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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	apiv1alpha1 "github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/internal/controller"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	admwebhook "github.com/red-hat-storage/ocs-client-operator/pkg/webhook"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	replicationv1alpha1 "github.com/csi-addons/kubernetes-csi-addons/api/replication.storage/v1alpha1"
	groupsnapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumegroupsnapshot/v1beta1"
	snapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	nbapis "github.com/noobaa/noobaa-operator/v5/pkg/apis"
	configv1 "github.com/openshift/api/config/v1"
	consolev1 "github.com/openshift/api/console/v1"
	quotav1 "github.com/openshift/api/quota/v1"
	secv1 "github.com/openshift/api/security/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	ramenv1alpha1 "github.com/ramendr/ramen/api/v1alpha1"
	admrv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metrics "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(snapapi.AddToScheme(scheme))
	utilruntime.Must(configv1.AddToScheme(scheme))
	utilruntime.Must(secv1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(apiv1alpha1.AddToScheme(scheme))
	utilruntime.Must(monitoringv1.AddToScheme(scheme))
	utilruntime.Must(consolev1.AddToScheme(scheme))
	utilruntime.Must(opv1a1.AddToScheme(scheme))
	utilruntime.Must(extv1.AddToScheme(scheme))
	utilruntime.Must(quotav1.AddToScheme(scheme))
	utilruntime.Must(csiopv1a1.AddToScheme(scheme))
	utilruntime.Must(nbapis.AddToScheme(scheme))
	utilruntime.Must(ramenv1alpha1.AddToScheme(scheme))
	utilruntime.Must(replicationv1alpha1.AddToScheme(scheme))
	utilruntime.Must(groupsnapapi.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var consolePort int
	var webhookPort int
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.IntVar(&webhookPort, "webhook-port", 7443, "The port the webhook sever binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.IntVar(&consolePort, "console-port", 9001, "The port where the console server will be serving it's payload")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	subscriptionwebhookSelector := fields.SelectorFromSet(fields.Set{"metadata.name": templates.SubscriptionWebhookName})
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metrics.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "7cb6f2e5.ocs.openshift.io",
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&admrv1.ValidatingWebhookConfiguration{}: {
					// only cache our validation webhook
					Field: subscriptionwebhookSelector,
				},
			},
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    webhookPort,
			CertDir: "/etc/tls/private",
		}),
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// set namespace
	err = utils.ValidateOperatorNamespace()
	if err != nil {
		setupLog.Error(err, "unable to validate operator namespace")
		os.Exit(1)
	}

	err = utils.ValidateStausReporterImage()
	if err != nil {
		setupLog.Error(err, "unable to validate status reporter image")
		os.Exit(1)
	}

	// apiclient.New() returns a client without cache. cache is not initialized before mgr.Start()
	// we need this because we need to watch for CRDs the operator is dependent on
	apiClient, err := client.New(mgr.GetConfig(), client.Options{
		Scheme: mgr.GetScheme(),
	})
	if err != nil {
		setupLog.Error(err, "Unable to get Client")
		os.Exit(1)
	}

	availCrds, err := getAvailableCRDNames(context.Background(), apiClient)
	if err != nil {
		setupLog.Error(err, "Unable get a list of available CRD names")
		os.Exit(1)
	}

	setupLog.Info("setting up webhook server")
	hookServer := mgr.GetWebhookServer()

	setupLog.Info("registering Subscription Channel validating webhook endpoint")
	hookServer.Register("/validate-subscription", &webhook.Admission{
		Handler: &admwebhook.SubscriptionAdmission{
			Client:  mgr.GetClient(),
			Decoder: admission.NewDecoder(mgr.GetScheme()),
			Log:     mgr.GetLogger().WithName("webhook.subscription"),
		}},
	)

	if err = (&controller.StorageClientReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		OperatorNamespace: utils.GetOperatorNamespace(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "StorageClient")
		os.Exit(1)
	}

	if err = (&controller.StorageClaimReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		OperatorNamespace: utils.GetOperatorNamespace(),
		AvailableCrds:     availCrds,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "StorageClaim")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	if err = (&controller.OperatorConfigMapReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		OperatorNamespace: utils.GetOperatorNamespace(),
		ConsolePort:       int32(consolePort),
		AvailableCrds:     availCrds,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "OperatorConfigMapReconciler")
		os.Exit(1)
	}

	if availCrds[controller.MaintenanceModeCRDName] {
		if err = (&controller.MaintenanceModeReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "MaintenanceMode")
			os.Exit(1)
		}
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func getAvailableCRDNames(ctx context.Context, cl client.Client) (map[string]bool, error) {
	crdExist := map[string]bool{}
	crdList := &metav1.PartialObjectMetadataList{}
	crdList.SetGroupVersionKind(extv1.SchemeGroupVersion.WithKind("CustomResourceDefinitionList"))
	if err := cl.List(ctx, crdList); err != nil {
		return nil, fmt.Errorf("error listing CRDs, %v", err)
	}
	// Iterate over the list and populate the map
	for i := range crdList.Items {
		crdExist[crdList.Items[i].Name] = true
	}
	return crdExist, nil
}
