package controller

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/console"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	configv1 "github.com/openshift/api/config/v1"
	secv1 "github.com/openshift/api/security/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	testNamespace         = "test-ns"
	fake417ClusterVersion = "4.17.0"
)

var fake418ImageSet = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "418-config",
		Namespace: testNamespace,
		Labels: map[string]string{
			csiImagesConfigMapLabel: "4.18.1",
		},
	},
}

var fake417ImageSet = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "417-config",
		Namespace: testNamespace,
		Labels: map[string]string{
			csiImagesConfigMapLabel: "4.17.2",
		},
	},
}

var fake416ImageSet = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "416-config",
		Namespace: testNamespace,
		Labels: map[string]string{
			csiImagesConfigMapLabel: "4.16.3",
		},
	},
}

var fake415ImageSet = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "415-config",
		Namespace: testNamespace,
		Labels: map[string]string{
			csiImagesConfigMapLabel: "4.15",
		},
	},
}

func newFakeScheme(t *testing.T) *runtime.Scheme {
	scheme := runtime.NewScheme()

	err := kubescheme.AddToScheme(scheme)
	assert.Nil(t, err, "failed to add kube scheme")

	err = configv1.AddToScheme(scheme)
	assert.Nil(t, err, "failed to add OCP config scheme")

	err = secv1.AddToScheme(scheme)
	assert.Nil(t, err, "failed to add OCP security scheme")

	err = v1alpha1.AddToScheme(scheme)
	assert.Nil(t, err, "failed to add v1alpha1 scheme")

	return scheme
}

func newFakeConfigMapReconciler(t *testing.T) OperatorConfigMapReconciler {
	var reconciler OperatorConfigMapReconciler

	reconciler.Scheme = newFakeScheme(t)
	reconciler.log = ctrllog.Log.WithName("configmap_controller_test")
	reconciler.OperatorNamespace = testNamespace

	return reconciler
}

func newFakeClientBuilder(scheme *runtime.Scheme) *fake.ClientBuilder {
	return fake.NewClientBuilder().
		WithScheme(scheme)
}

func TestGetImageSet(t *testing.T) {
	r := newFakeConfigMapReconciler(t)

	r.Client = newFakeClientBuilder(r.Scheme).
		Build()
	_, err := r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.NotNil(t, err, "should fail when no imageset configmaps exist")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		Build()
	_, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.NotNil(t, err, "should fail when imageset configmaps is ahead of platform")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		WithRuntimeObjects(fake417ImageSet).
		Build()
	cm, err := r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when imageset at exact version exists")
	assert.Equal(t, fake417ImageSet.Name, cm, "should pick exact 417 imageset")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		WithRuntimeObjects(fake417ImageSet).
		WithRuntimeObjects(fake416ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when imageset at exact version exists")
	assert.Equal(t, fake417ImageSet.Name, cm, "should prefer 417 imageset")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		WithRuntimeObjects(fake416ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when compatible imagesets  exists")
	assert.Equal(t, fake416ImageSet.Name, cm, "should prefer 416 imageset which is closer to 417")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		WithRuntimeObjects(fake416ImageSet).
		WithRuntimeObjects(fake415ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when compatible imagesets  exists")
	assert.Equal(t, fake416ImageSet.Name, cm, "should prefer 416 imageset which is closer to 417")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake417ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when imageset at exact version exists")
	assert.Equal(t, fake417ImageSet.Name, cm)

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake415ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when compatible imagesets  exists")
	assert.Equal(t, fake415ImageSet.Name, cm, "should prefer 415 imageset as it is lesser than 417 and nothing closer exists")
}

func TestTopologyLabelsFromConfigMap(t *testing.T) {
	tests := []struct {
		name            string
		configMapLabels string
		storageClients  []v1alpha1.StorageClient
		expectedLabels  []string
	}{
		{
			name:            "ConfigMap only when no status",
			configMapLabels: "zone,region",
			storageClients:  []v1alpha1.StorageClient{},
			expectedLabels:  []string{"zone", "region"},
		},
		{
			name:            "Status overrides ConfigMap",
			configMapLabels: "zone,region",
			storageClients: []v1alpha1.StorageClient{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "client1"},
					Status: v1alpha1.StorageClientStatus{
						RbdDriverRequirements: &v1alpha1.RbdDriverRequirements{
							TopologyDomainLabels: []string{"rack", "host"},
						},
					},
				},
			},
			expectedLabels: []string{"rack", "host"},
		},
		{
			name:            "Multiple clients merge status",
			configMapLabels: "zone,region",
			storageClients: []v1alpha1.StorageClient{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "client1"},
					Status: v1alpha1.StorageClientStatus{
						RbdDriverRequirements: &v1alpha1.RbdDriverRequirements{
							TopologyDomainLabels: []string{"rack"},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "client2"},
					Status: v1alpha1.StorageClientStatus{
						RbdDriverRequirements: &v1alpha1.RbdDriverRequirements{
							TopologyDomainLabels: []string{"datacenter"},
						},
					},
				},
			},
			expectedLabels: []string{"rack", "datacenter"},
		},
		{
			name:            "Empty status uses ConfigMap",
			configMapLabels: "zone,region",
			storageClients: []v1alpha1.StorageClient{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "client1"},
					Status: v1alpha1.StorageClientStatus{
						RbdDriverRequirements: nil,
					},
				},
			},
			expectedLabels: []string{"zone", "region"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newFakeConfigMapReconciler(t)
			r.operatorConfigMap = &corev1.ConfigMap{
				Data: map[string]string{
					utils.TopologyFailureDomainLabelsKey: tt.configMapLabels,
				},
			}

			result := r.getTopologyLabels(&v1alpha1.StorageClientList{Items: tt.storageClients})

			assert.Equal(t, len(tt.expectedLabels), len(result))
			for _, label := range tt.expectedLabels {
				_, exists := result[label]
				assert.True(t, exists, "expected label "+label)
			}
		})
	}
}

func TestParseEndpointConfigs(t *testing.T) {
	validCfg := s3EndpointConfig{EndpointURL: "https://noobaa-s3.example.com"}
	validCfgBytes, err := json.Marshal(validCfg)
	assert.NoError(t, err)

	tests := []struct {
		name      string
		input     map[string]string
		expectErr bool
		expectLen int
	}{
		{
			name: "valid input skips empty values",
			input: map[string]string{
				"noobaaS3": string(validCfgBytes),
				"":         string(validCfgBytes),
				"   ":      string(validCfgBytes),
				"rgwS3":    "   ",
			},
			expectErr: false,
			expectLen: 1,
		},
		{
			name: "invalid json payload",
			input: map[string]string{
				"noobaa": "{invalid-json}",
			},
			expectErr: true,
			expectLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, parseErr := parseEndpointConfigs(tt.input)
			if tt.expectErr {
				assert.Error(t, parseErr)
				assert.Contains(t, parseErr.Error(), "decode endpoint config for key")
				assert.Len(t, out, tt.expectLen)
			} else {
				assert.NoError(t, parseErr)
				assert.Len(t, out, tt.expectLen)
				assert.Equal(t, validCfg.EndpointURL, out["noobaaS3"].EndpointURL)
			}
		})
	}
}

func TestBuildS3EndpointProxyConfigForClient(t *testing.T) {
	tests := []struct {
		name             string
		secretData       map[string][]byte
		endpoints        map[string]s3EndpointConfig
		expectedIncludes []string
		expectedExcludes []string
		expectErr        bool
	}{
		{
			name: "builds config for valid https endpoints only",
			secretData: map[string][]byte{
				"client-1-noobaaS3.crt": []byte("dummy-data"),
			},
			endpoints: map[string]s3EndpointConfig{
				"noobaaS3": {
					EndpointURL: "https://noobaa-s3.example.com",
				},
				"noobaaS3Vectors": {
					EndpointURL: "https://noobaa-s3-vectors.example.com",
				},
				"rgwS3": {
					EndpointURL: "://invalid",
				},
			},
			expectedIncludes: []string{
				"location /client-1/noobaaS3/",
				"location /client-1/noobaaS3Vectors/",
			},
			expectedExcludes: []string{
				"://invalid",
				"location /client-1/rgwS3/",
			},
			expectErr: false,
		},
		{
			name:       "returns empty config when no valid endpoint",
			secretData: map[string][]byte{},
			endpoints: map[string]s3EndpointConfig{
				"rgwS3": {
					EndpointURL: "://invalid",
				},
			},
			expectedIncludes: []string{},
			expectedExcludes: []string{"location /client-1/rgwS3/", "://invalid"},
			expectErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newFakeConfigMapReconciler(t)
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      s3EndpointCASecretName,
					Namespace: testNamespace,
					UID:       "secret-uid",
				},
				Data: tt.secretData,
			}
			r.Client = newFakeClientBuilder(r.Scheme).
				WithRuntimeObjects(secret).
				Build()

			content, buildErr := r.buildS3EndpointProxyConfigForClient("client-1", tt.endpoints)
			if tt.expectErr {
				assert.Error(t, buildErr)
			} else {
				assert.NoError(t, buildErr)
				for _, expected := range tt.expectedIncludes {
					assert.Contains(t, content, expected)
				}
				for _, excluded := range tt.expectedExcludes {
					assert.NotContains(t, content, excluded)
				}
			}
		})
	}
}

func TestBuildDesiredNginxDataWithProxies(t *testing.T) {
	tests := []struct {
		name              string
		operatorConfigMap map[string]string
		endpointConfig    map[string]string
		expectErr         bool
	}{
		{
			name: "proxy disabled via operator configmap",
			operatorConfigMap: map[string]string{
				disableS3EndpointProxyKey: "true",
			},
			endpointConfig: map[string]string{
				"noobaaS3": `{"endpointUrl":"https://noobaa-s3.example.com"}`,
			},
			expectErr: false,
		},
		{
			name:              "invalid endpoint json",
			operatorConfigMap: map[string]string{},
			endpointConfig: map[string]string{
				"noobaaS3": "{invalid-json}",
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newFakeConfigMapReconciler(t)
			r.operatorConfigMap = &corev1.ConfigMap{Data: tt.operatorConfigMap}

			labeledCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "client-1",
					Namespace: testNamespace,
					Labels: map[string]string{
						s3EndpointsConfigMapLabelKey: strconv.FormatBool(true),
					},
				},
				Data: tt.endpointConfig,
			}
			r.Client = newFakeClientBuilder(r.Scheme).
				WithRuntimeObjects(labeledCM).
				Build()

			out, err := r.buildDesiredNginxDataWithProxies()
			if tt.expectErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "parse endpoints ConfigMap")
				assert.Equal(t, console.GetNginxRootConf(), out["nginx.conf"])
			} else {
				assert.NoError(t, err)
				assert.Equal(t, map[string]string{"nginx.conf": console.GetNginxRootConf()}, out)
			}
		})
	}
}
