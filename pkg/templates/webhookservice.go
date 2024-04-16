package templates

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// should be <namePrefix from config/default/kustomization><.metadata.name from config/manager/webhook_service.yaml>
	WebhookServiceName = "ocs-client-operator-webhook-server"
)

// should match the spec at config/manager/webhook_service.yaml
var WebhookService = corev1.Service{
	Spec: corev1.ServiceSpec{
		Ports: []corev1.ServicePort{
			{
				Name:       "https",
				Port:       443,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.FromInt32(7443),
			},
		},
		Selector: map[string]string{
			"app": "ocs-client-operator",
		},
		Type: corev1.ServiceTypeClusterIP,
	},
}
