package templates

import (
	admrv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/utils/ptr"
)

var SubscriptionValidatingWebhook = admrv1.ValidatingWebhook{
	Name: "subscription.ocs.openshift.io",
	ClientConfig: admrv1.WebhookClientConfig{
		Service: &admrv1.ServiceReference{
			Name: "ocs-client-operator-webhook-server",
			Path: ptr.To("/validate-subscription"),
			Port: ptr.To(int32(443)),
		},
	},
	Rules: []admrv1.RuleWithOperations{
		{
			Rule: admrv1.Rule{
				APIGroups:   []string{"operators.coreos.com"},
				APIVersions: []string{"v1alpha1"},
				Resources:   []string{"subscriptions"},
				Scope:       ptr.To(admrv1.NamespacedScope),
			},
			Operations: []admrv1.OperationType{admrv1.Update},
		},
	},
	SideEffects:             ptr.To(admrv1.SideEffectClassNone),
	TimeoutSeconds:          ptr.To(int32(30)),
	AdmissionReviewVersions: []string{"v1"},
}
