package console

import (
	"fmt"

	consolev1alpha1 "github.com/openshift/api/console/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var (
	DeploymentName     = "ocs-client-operator-console"
	NginxConfigMapName = fmt.Sprintf("%s-nginx-conf", DeploymentName)
	PluginName         = "odf-client-console"

	pluginBasePath          = "/"
	pluginDisplayName       = "ODF Client Console"
	servicePortName         = "console-port"
	serviceSecretAnnotation = "service.alpha.openshift.io/serving-cert-secret-name"
	serviceLabelKey         = "app.kubernetes.io/name"
)

var consoleServiceSpec = apiv1.ServiceSpec{
	Ports: []apiv1.ServicePort{
		{
			Protocol: apiv1.ProtocolTCP,
			Name:     servicePortName,
		},
	},
	Selector: map[string]string{
		serviceLabelKey: DeploymentName,
	},
}

func SetConsoleServiceDesiredState(service *apiv1.Service, port int32) {
	service.SetAnnotations(map[string]string{
		serviceSecretAnnotation: fmt.Sprintf("%s-serving-cert", DeploymentName),
	})
	service.SetLabels(map[string]string{
		serviceLabelKey: DeploymentName,
	})
	consoleServiceSpec.DeepCopyInto(&service.Spec)
	servicePort := utils.Find(consoleServiceSpec.Ports, func(port *apiv1.ServicePort) bool {
		return port.Name == servicePortName
	})
	servicePort.TargetPort = intstr.IntOrString{IntVal: port}
	servicePort.Port = port
}

var consolePluginSpec = consolev1alpha1.ConsolePluginSpec{
	DisplayName: pluginDisplayName,
	Service: consolev1alpha1.ConsolePluginService{
		Name:     DeploymentName,
		BasePath: pluginBasePath,
	},
}

func SetConsolePluginDesiredState(plugin *consolev1alpha1.ConsolePlugin, consolePort int32, serviceNamespace string) {
	consolePluginSpec.DeepCopyInto(&plugin.Spec)
	consolePluginSpec.Service.Port = consolePort
	consolePluginSpec.Service.Namespace = serviceNamespace
}

func SetNgnixConfigMapDesiredState(cm *apiv1.ConfigMap) {
	cm.Data = map[string]string{
		"nginx.conf": nginxConf,
	}
}
