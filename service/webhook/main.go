package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlLog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	//+kubebuilder:scaffold:imports
)

const (
	desiredSubscriptionChannelAnnotationKey = "ocs.openshift.io/subscription.channel"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	ctrlLog.SetLogger(zap.New())
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	setupLog := ctrl.Log.WithName("setup")
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    9443,
			CertDir: "/etc/tls/private",
		}),
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	hookServer := mgr.GetWebhookServer()
	setupLog.Info("registering Subscription Channel validating webhook endpoint")
	hookServer.Register("/validate-subscription", &webhook.Admission{
		Handler: &SubscriptionAdmission{
			Client:  mgr.GetClient(),
			Decoder: admission.NewDecoder(mgr.GetScheme()),
		}},
	)

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "failed to start manager")
		os.Exit(1)
	}
}

type SubscriptionAdmission struct {
	Client  client.Client
	Decoder *admission.Decoder
}

func (s *SubscriptionAdmission) Handle(ctx context.Context, req admission.Request) admission.Response {
	subscription := &opv1a1.Subscription{}
	if err := s.Decoder.Decode(req, subscription); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("only subscriptions admission reviews are supported: %v", err))
	}

	if subscription.Spec.Package != "ocs-client-operator" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("only ocs-client-operator subscription validation is supported"))
	}

	storageClients := &v1alpha1.StorageClientList{}
	if err := s.Client.List(ctx, storageClients); err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to list storageclients for validating subscription request: %v", err))
	}

	for idx := range storageClients.Items {
		storageClient := &storageClients.Items[idx]
		if storageClient.GetAnnotations()[desiredSubscriptionChannelAnnotationKey] != subscription.Spec.Channel {
			return admission.Denied(fmt.Sprintf("subscription channel %q not allowed as it'll violate storageclient %q requirements", subscription.Spec.Channel, client.ObjectKeyFromObject(storageClient)))
		}
	}

	return admission.Allowed(fmt.Sprintf("valid subscription channel: %q", subscription.Spec.Channel))
}
