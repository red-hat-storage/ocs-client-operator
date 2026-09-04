package templates

import (
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	// should be <namePrefix from config/default/kustomization><.metadata.name from config/prometheus/monitor.yaml>
	MetricsServiceMonitorName = "ocs-client-operator-metrics-monitor"
	// should be <namePrefix from config/default/kustomization><.metadata.name from config/default/metrics_service.yaml>
	MetricsServiceName = "ocs-client-operator-metrics"
)

// MetricsServiceMonitor should match the endpoint/selector at config/prometheus/monitor.yaml.
// The tlsConfig's serverName and CA are set at reconcile time once the operator namespace is known.
var MetricsServiceMonitor = monitoringv1.ServiceMonitor{
	Spec: monitoringv1.ServiceMonitorSpec{
		Endpoints: []monitoringv1.Endpoint{
			{
				Path:            "/metrics",
				Port:            "https",
				Scheme:          ptr.To(monitoringv1.SchemeHTTPS),
				BearerTokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token",
			},
		},
		Selector: metav1.LabelSelector{
			MatchLabels: map[string]string{
				"app":    "ocs-client-operator",
				"server": "metrics",
			},
		},
	},
}
