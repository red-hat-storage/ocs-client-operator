package templates

import (
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	// the ServiceMonitor is now reconciled from code instead of being shipped via the CSV
	// (config/prometheus is no longer referenced from config/default/kustomization.yaml), but the name is
	// kept the same as the old CSV-deployed one so that on OLM upgrade the old resource is deleted and
	// this one is created in its place
	OperatorServiceMonitorName = "ocs-client-operator-metrics-monitor"
	// should be <namePrefix from config/default/kustomization><.metadata.name from config/default/metrics_service.yaml>
	MetricsServiceName = "ocs-client-operator-metrics"
)

// OperatorServiceMonitor should match the endpoint/selector at config/prometheus/monitor.yaml.
// The tlsConfig's serverName and CA are set at reconcile time once the operator namespace is known.
var OperatorServiceMonitor = monitoringv1.ServiceMonitor{
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
