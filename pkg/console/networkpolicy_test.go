package console

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestGetNetworkPolicy(t *testing.T) {
	protocol := corev1.ProtocolTCP
	expected := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: DeploymentName, Namespace: testNamespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{AppNameLabelKey: DeploymentName}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "openshift-console"}},
					PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "console", "component": "ui"}},
				}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &protocol, Port: &intstr.IntOrString{IntVal: 9001}}},
			}},
		},
	}

	if actual := GetNetworkPolicy(testNamespace); !reflect.DeepEqual(actual, expected) {
		t.Errorf("unexpected network policy: %#v", actual)
	}
}

const testNamespace = "test-ns"
