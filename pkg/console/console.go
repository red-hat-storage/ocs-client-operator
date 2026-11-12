package console

import (
	_ "embed"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/template"

	consolev1 "github.com/openshift/api/console/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// DefaultNginxWorkerProcesses is used when CONSOLE_NGINX_WORKER_PROCESSES
	// is unset or invalid. A fixed value avoids OOM/FD exhaustion on high-CPU
	// nodes where "auto" would spawn one worker per CPU.
	DefaultNginxWorkerProcesses = "8"

	// NginxWorkerProcessesEnvVar overrides DefaultNginxWorkerProcesses when set
	// on the operator pod via Subscription spec.config.env. Accepted values are
	// a positive integer or "auto".
	NginxWorkerProcessesEnvVar = "CONSOLE_NGINX_WORKER_PROCESSES"
)

var (
	DeploymentName = "ocs-client-operator-console"
	pluginBasePath = "/"

	NginxConfigMapName = fmt.Sprintf("%s-nginx-conf", DeploymentName)
	pluginName         = "odf-client-console"

	pluginDisplayName = "ODF Client Console"

	servicePortName         = "console-port"
	serviceSecretAnnotation = "service.alpha.openshift.io/serving-cert-secret-name"

	AppNameLabelKey = "app.kubernetes.io/name"
)

//go:embed nginx_proxy.tmpl
var nginxProxyConf string

//go:embed nginx_root.tmpl
var nginxRootTmplText string

var nginxRootTmpl = template.Must(template.New("nginxRoot").Parse(nginxRootTmplText))

func GetService(port int32, namespace string) *apiv1.Service {
	return &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DeploymentName,
			Namespace: namespace,
			Annotations: map[string]string{
				serviceSecretAnnotation: fmt.Sprintf("%s-serving-cert", DeploymentName),
			},
			Labels: map[string]string{
				AppNameLabelKey: DeploymentName,
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
				AppNameLabelKey: DeploymentName,
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
					Port:      consolePort,
					BasePath:  pluginBasePath,
				},
			},
			Proxy: getConsolePluginProxy(consolePort, serviceNamespace),
		},
	}
}

// GetNginxWorkerProcesses returns the nginx worker_processes value.
// Prefer CONSOLE_NGINX_WORKER_PROCESSES when it is a positive integer or "auto";
// otherwise fall back to DefaultNginxWorkerProcesses.
func GetNginxWorkerProcesses() string {
	value := strings.TrimSpace(os.Getenv(NginxWorkerProcessesEnvVar))
	if value == "" {
		return DefaultNginxWorkerProcesses
	}
	if strings.EqualFold(value, "auto") {
		return "auto"
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return DefaultNginxWorkerProcesses
	}
	return strconv.Itoa(n)
}

func GetNginxRootConf() string {
	var sb strings.Builder
	data := struct {
		WorkerProcesses string
	}{
		WorkerProcesses: GetNginxWorkerProcesses(),
	}
	if err := nginxRootTmpl.Execute(&sb, data); err != nil {
		// template is validated at init via template.Must; this should not happen
		panic(fmt.Sprintf("failed to render nginx root config: %v", err))
	}
	return sb.String()
}

func GetNginxProxyConf(uniqueIdentifier, exposeAs, endpointURL, endpointHost, certsPath string) (string, error) {
	type nginxProxyConfData struct {
		UniqueIdentifier string
		ExposeAs         string
		EndpointURL      string
		EndpointHost     string
		CertsPath        string
	}

	data := nginxProxyConfData{
		UniqueIdentifier: uniqueIdentifier,
		ExposeAs:         exposeAs,
		EndpointURL:      endpointURL,
		EndpointHost:     endpointHost,
		CertsPath:        certsPath,
	}

	t, err := template.New("nginxProxyConf").Parse(nginxProxyConf)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := t.Execute(&sb, data); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func getConsolePluginProxy(port int32, serviceNamespace string) []consolev1.ConsolePluginProxy {
	return []consolev1.ConsolePluginProxy{
		{
			Alias: "s3EndpointProxy",
			Endpoint: consolev1.ConsolePluginProxyEndpoint{
				Type: consolev1.ProxyTypeService,
				Service: &consolev1.ConsolePluginProxyServiceConfig{
					Name:      DeploymentName,
					Namespace: serviceNamespace,
					Port:      port,
				},
			},
			Authorization: consolev1.None,
		},
	}
}
