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
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"strings"

	apiv1alpha1 "github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/internal/controller"
	"github.com/red-hat-storage/ocs-client-operator/internal/controller/alert"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	admwebhook "github.com/red-hat-storage/ocs-client-operator/pkg/webhook"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	csiopv1 "github.com/ceph/ceph-csi-operator/api/v1"
	csiaddonsv1alpha1 "github.com/csi-addons/kubernetes-csi-addons/api/csiaddons/v1alpha1"
	replicationv1alpha1 "github.com/csi-addons/kubernetes-csi-addons/api/replication.storage/v1alpha1"
	groupsnapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumegroupsnapshot/v1beta1"
	snapapi "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	nbv1 "github.com/noobaa/noobaa-operator/v5/pkg/apis/noobaa/v1alpha1"
	configv1 "github.com/openshift/api/config/v1"
	consolev1 "github.com/openshift/api/console/v1"
	quotav1 "github.com/openshift/api/quota/v1"
	secv1 "github.com/openshift/api/security/v1"
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	ramenv1alpha1 "github.com/ramendr/ramen/api/v1alpha1"
	odfgsapiv1b1 "github.com/red-hat-storage/external-snapshotter/client/v8/apis/volumegroupsnapshot/v1beta1"
	ocstlsv1 "github.com/red-hat-storage/ocs-tls-profiles/api/v1"
	admrv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
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
	utilruntime.Must(csiopv1.AddToScheme(scheme))
	utilruntime.Must(ramenv1alpha1.AddToScheme(scheme))
	utilruntime.Must(replicationv1alpha1.AddToScheme(scheme))
	utilruntime.Must(groupsnapapi.AddToScheme(scheme))
	utilruntime.Must(odfgsapiv1b1.AddToScheme(scheme))
	utilruntime.Must(csiaddonsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(ocstlsv1.AddToScheme(scheme))
	// ObjectBucketClaim/ObjectBucket (objectbucket.io); nbapis.AddToScheme does not register these types
	// this part was added to avoid direct import of lib-bucket-provisioner
	objectBucketGV := schema.GroupVersion{Group: "objectbucket.io", Version: "v1alpha1"}
	scheme.AddKnownTypes(objectBucketGV,
		&nbv1.ObjectBucketClaim{},
		&nbv1.ObjectBucketClaimList{},
		&nbv1.ObjectBucket{},
		&nbv1.ObjectBucketList{},
	)
	metav1.AddToGroupVersion(scheme, objectBucketGV)
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr, healthProbeAddr string
	var webhookPort, consolePort int

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "The address the metrics endpoint binds to.")
	flag.StringVar(&healthProbeAddr, "health-probe-bind-address", ":8081", "The address the health probe endpoint binds to.")
	flag.IntVar(&webhookPort, "webhook-port", 7443, "The port the webhook sever binds to.")
	flag.IntVar(&consolePort, "console-port", 9001, "The port where the console server will be serving it's payload")

	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	defaultNamespaces := map[string]cache.Config{}
	operatorNamespace := utils.GetOperatorNamespace()
	defaultNamespaces[operatorNamespace] = cache.Config{}

	watchNamespace := utils.GetWatchNamespace()
	if watchNamespace == "" {
		setupLog.Info("No value for env WATCH_NAMESPACE is set. Manager will only watch for resources in the operator deployed namespace.")
	} else {
		for _, namespace := range strings.Split(watchNamespace, ",") {
			defaultNamespaces[namespace] = cache.Config{}
		}
	}

	apiCtx := context.Background()
	// apiclient.New() returns a client without cache. cache is not initialized before mgr.Start()
	// we need this because we need to watch for CRDs the operator is dependent on
	apiClient, err := client.New(ctrl.GetConfigOrDie(), client.Options{
		Scheme: scheme,
	})
	if err != nil {
		setupLog.Error(err, "Unable to get API client")
		os.Exit(1)
	}
	availCrds, err := getAvailableCRDNames(apiCtx, apiClient)
	if err != nil {
		setupLog.Error(err, "Unable get a list of available CRD names")
		os.Exit(1)
	}

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

	podName, err := utils.GetOperatorPodName()
	if err != nil {
		setupLog.Error(err, "Failed to get operator pod name")
	}

	var initialTLSGeneration int64
	startupProfile := &ocstlsv1.TLSProfile{}
	startupProfile.Name = controller.TLSProfileName
	startupProfile.Namespace = utils.GetOperatorNamespace()
	if err := apiClient.Get(apiCtx, client.ObjectKeyFromObject(startupProfile), startupProfile); err != nil {
		if !kerrors.IsNotFound(err) {
			setupLog.Error(err, "failed to get TLSProfile at startup")
			os.Exit(1)
		}
		startupProfile = nil
	} else {
		initialTLSGeneration = startupProfile.Generation
	}

	webhookTlsOpts, err := buildServerTLSOpts(startupProfile, "ocs.openshift.io", "webhook")
	if err != nil {
		setupLog.Error(err, "invalid TLSProfile config for webhook server")
		os.Exit(1)
	}
	metricsTlsOpts, err := buildServerTLSOpts(startupProfile, "ocs.openshift.io", "metrics")
	if err != nil {
		setupLog.Error(err, "invalid TLSProfile config for metrics server")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Cache:  buildCacheAvailableCRDs(availCrds, defaultNamespaces, operatorNamespace),

		// servers
		HealthProbeBindAddress: healthProbeAddr,
		Metrics: metricsserver.Options{
			BindAddress:    metricsAddr,
			SecureServing:  true,
			CertDir:        "/tmp/metrics/tls/private",
			FilterProvider: filters.WithAuthenticationAndAuthorization,
			TLSOpts:        metricsTlsOpts,
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    webhookPort,
			CertDir: "/tmp/webhook/tls/private",
			TLSOpts: webhookTlsOpts,
		}),
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	if isAPIResourceAvailable(mgr.GetRESTMapper(), schema.GroupVersion{Group: "storage.k8s.io", Version: "v1"}, "volumeattributesclasses") {
		availCrds[controller.VolumeAttributesClassResourceName] = true
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
		OperatorPodName:   podName,
		AvailableCrds:     availCrds,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "StorageClient")
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

	alertRunnable := alert.NewRunnable(
		mgr.GetClient(),
		utils.GetOperatorNamespace(),
		ctrl.Log.WithName("alert"),
		alert.DefaultPollInterval,
	)
	if err := mgr.Add(alertRunnable); err != nil {
		setupLog.Error(err, "unable to add alert runnable to manager")
		os.Exit(1)
	}

	mgrCtx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancel()
	shutdownContainer := func() {
		cancel()
	}

	if err = (&controller.OperatorConfigMapReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		OperatorNamespace:       utils.GetOperatorNamespace(),
		ConsolePort:             int32(consolePort),
		AvailableCrds:           availCrds,
		UpdateAlertPollInterval: alertRunnable.SetPollInterval,
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

	if err = (&controller.CrdsPresenceReconciler{
		Client:            mgr.GetClient(),
		AvailableCrds:     availCrds,
		ShutdownContainer: shutdownContainer,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CrdsPresence")
		os.Exit(1)
	}

	if availCrds[controller.ObjectBucketClaimCrdName] {
		if err = (&controller.ObcReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "ObjectBucketClaim")
			os.Exit(1)
		}
	}

	if err = (&controller.TLSProfileReconciler{
		Client:            mgr.GetClient(),
		ShutdownContainer: shutdownContainer,
		InitialGeneration: initialTLSGeneration,
		Namespace:         utils.GetOperatorNamespace(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TLSProfile")
		os.Exit(1)
	}

	alertCollector := alert.NewCollector(alertRunnable)
	resourceCollector := alert.NewResourceCollector(mgr.GetClient())
	metrics.Registry.MustRegister(alertCollector, resourceCollector)

	setupLog.Info("starting manager")
	if err := mgr.Start(mgrCtx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func buildServerTLSOpts(profile *ocstlsv1.TLSProfile, domain, server string) ([]func(*tls.Config), error) {
	if profile == nil {
		return nil, nil
	}
	tlsConfig, exist := ocstlsv1.GetConfigForServer(profile, domain, server)
	if !exist {
		return nil, nil
	}
	if err := ocstlsv1.ValidateTLSConfig(tlsConfig); err != nil {
		return nil, err
	}
	goTLS := ocstlsv1.GetGoTLSConfig(tlsConfig)
	return []func(*tls.Config){
		func(cfg *tls.Config) {
			cfg.MinVersion = goTLS.MinVersion
			cfg.MaxVersion = goTLS.MaxVersion
			cfg.CipherSuites = goTLS.CipherSuites
			cfg.CurvePreferences = goTLS.CurvePreferences
		},
	}, nil
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

func buildCacheAvailableCRDs(
	availCrds map[string]bool,
	defaultNamespaces map[string]cache.Config,
	operatorNamespace string,
) cache.Options {
	subscriptionwebhookSelector := fields.SelectorFromSet(fields.Set{"metadata.name": templates.SubscriptionWebhookName})
	noobaaLabelSelector := labels.SelectorFromSet(labels.Set{"app": "noobaa"})
	configMapAndSecretCacheByNamespace := map[string]cache.Config{
		operatorNamespace: {},
		cache.AllNamespaces: {
			LabelSelector: noobaaLabelSelector,
		},
	}
	cacheAvailableCrd := cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&admrv1.ValidatingWebhookConfiguration{}: {
				// only cache our validation webhook
				Field: subscriptionwebhookSelector,
			},
			&corev1.ConfigMap{}: {
				Namespaces: configMapAndSecretCacheByNamespace,
			},
			&corev1.Secret{}: {
				Namespaces: configMapAndSecretCacheByNamespace,
			},
		},
		DefaultNamespaces: defaultNamespaces,
	}
	// Watch ObjectBucketClaim in all namespaces so OBC controller reconciles regardless of WATCH_NAMESPACE.
	// Empty ByObject would be defaulted to DefaultNamespaces; explicitly set NamespaceAll to avoid that.
	if availCrds[controller.ObjectBucketClaimCrdName] {
		cacheAvailableCrd.ByObject[&nbv1.ObjectBucketClaim{}] = cache.ByObject{
			Namespaces: map[string]cache.Config{corev1.NamespaceAll: {}},
		}
	}
	return cacheAvailableCrd
}

func isAPIResourceAvailable(mapper apimeta.RESTMapper, gv schema.GroupVersion, resource string) bool {
	resources, err := mapper.ResourcesFor(gv.WithResource(resource))
	return err == nil && len(resources) > 0
}
