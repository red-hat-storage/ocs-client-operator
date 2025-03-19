package console

import (
	"fmt"

	consolev1 "github.com/openshift/api/console/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var (
	DeploymentName = "ocs-client-operator-console"
	pluginBasePath = "/"

	NginxConfigMapName = fmt.Sprintf("%s-nginx-conf", DeploymentName)
	pluginName         = "odf-client-console"

	pluginDisplayName = "ODF Client Console"

	servicePortName         = "console-port"
	serviceSecretAnnotation = "service.alpha.openshift.io/serving-cert-secret-name"
	serviceLabelKey         = "app.kubernetes.io/name"
)

func GetService(port int32, namespace string) *apiv1.Service {
	return &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DeploymentName,
			Namespace: namespace,
			Annotations: map[string]string{
				serviceSecretAnnotation: fmt.Sprintf("%s-serving-cert", DeploymentName),
			},
			Labels: map[string]string{
				serviceLabelKey: DeploymentName,
			},
		},
		Spec: apiv1.ServiceSpec{
			Ports: []apiv1.ServicePort{
				{
					Protocol:   apiv1.ProtocolTCP,
					TargetPort: intstr.IntOrString{IntVal: port},
					Port:       port,
					Name:       servicePortName,
				},
			},
			Selector: map[string]string{
				serviceLabelKey: DeploymentName,
			},
		},
	}
}

func GetConsolePlugin(consolePort int32, serviceNamespace string) *consolev1.ConsolePlugin {
	return &consolev1.ConsolePlugin{
		ObjectMeta: metav1.ObjectMeta{
			Name: pluginName,
		},
		Spec: consolev1.ConsolePluginSpec{
			DisplayName: pluginDisplayName,
			I18n: consolev1.ConsolePluginI18n{
				LoadType: consolev1.Empty,
			},
			Backend: consolev1.ConsolePluginBackend{
				Type: consolev1.Service,
				Service: &consolev1.ConsolePluginService{
					Name:      DeploymentName,
					Namespace: serviceNamespace,
					Port:      int32(consolePort),
					BasePath:  pluginBasePath,
				},
			},
			Proxy: getConsolePluginProxy(serviceNamespace),
		},
	}
}

func GetNginxConf() string {
	return nginxConf
}

func GetNginxConfConfigMap(namespace string) *apiv1.ConfigMap {
	return &apiv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-nginx-conf", DeploymentName),
			Namespace: namespace,
		},
		Data: map[string]string{
			"nginx.conf": nginxConf,
		},
	}
}

func getConsolePluginProxy(serviceNamespace string) []consolev1.ConsolePluginProxy {
	return []consolev1.ConsolePluginProxy{
		{
			Alias: "s3",
			Endpoint: consolev1.ConsolePluginProxyEndpoint{
				Type: consolev1.ProxyTypeService,
				Service: &consolev1.ConsolePluginProxyServiceConfig{
					Name:      "s3-endpoint-proxy",
					Namespace: serviceNamespace,
					Port:      443,
				},
			},
			Authorization: consolev1.None,
		},
	}
}
