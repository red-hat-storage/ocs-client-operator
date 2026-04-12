package templates

const (
	// should be <namePrefix from config/default/kustomization.yaml><.metadata.name from config/default/metrics_service.yaml>
	MetricsServiceName = "ocs-client-operator-metrics"

	// should match .spec.ports[0].port from config/default/metrics_service.yaml
	MetricsServicePort int32 = 8080
)
