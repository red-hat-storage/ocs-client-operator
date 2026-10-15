/*
Copyright 2022 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testNamespace = "openshift-storage"

// testCertKeyPEM generates a self-signed certificate and its private key, PEM-encoded.
func testCertKeyPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func TestLoadClientTLSConfig(t *testing.T) {
	caPEM, _ := testCertKeyPEM(t)
	certPEM, keyPEM := testCertKeyPEM(t)

	caConfigMap := func(key string, data []byte) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "ca-cm", Namespace: testNamespace},
			Data:       map[string]string{key: string(data)},
		}
	}
	clientSecret := func(data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "client-cert", Namespace: testNamespace},
			Data:       data,
		}
	}
	clientWith := func(spec v1alpha1.StorageClientSpec) *v1alpha1.StorageClient {
		return &v1alpha1.StorageClient{
			ObjectMeta: metav1.ObjectMeta{Name: "client", Namespace: testNamespace},
			Spec:       spec,
		}
	}
	caRef := corev1.ConfigMapKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "ca-cm"},
		Key:                  "ca.crt",
	}
	certRef := corev1.LocalObjectReference{Name: "client-cert"}

	tests := []struct {
		name          string
		objs          []client.Object
		storageClient *v1alpha1.StorageClient
		wantErr       bool
		check         func(t *testing.T, cfg *tls.Config)
	}{
		{
			name:          "no CA, no client cert -> insecure",
			storageClient: clientWith(v1alpha1.StorageClientSpec{}),
			check: func(t *testing.T, cfg *tls.Config) {
				assert.True(t, cfg.InsecureSkipVerify)
				assert.Nil(t, cfg.RootCAs)
				assert.Empty(t, cfg.Certificates)
				assert.Empty(t, cfg.ServerName)
			},
		},
		{
			name:          "CA set, default SAN",
			objs:          []client.Object{caConfigMap("ca.crt", caPEM)},
			storageClient: clientWith(v1alpha1.StorageClientSpec{ServerCAConfigMap: caRef}),
			check: func(t *testing.T, cfg *tls.Config) {
				assert.False(t, cfg.InsecureSkipVerify)
				assert.NotNil(t, cfg.RootCAs)
				assert.Equal(t, defaultProviderServerSAN, cfg.ServerName)
			},
		},
		{
			name:          "CA set, explicit SAN overrides default",
			objs:          []client.Object{caConfigMap("ca.crt", caPEM)},
			storageClient: clientWith(v1alpha1.StorageClientSpec{ServerCAConfigMap: caRef, ServerSAN: "custom.san.svc"}),
			check: func(t *testing.T, cfg *tls.Config) {
				assert.Equal(t, "custom.san.svc", cfg.ServerName)
			},
		},
		{
			name: "CA + client cert -> mTLS",
			objs: []client.Object{
				caConfigMap("ca.crt", caPEM),
				clientSecret(map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM}),
			},
			storageClient: clientWith(v1alpha1.StorageClientSpec{ServerCAConfigMap: caRef, ClientCertSecret: certRef}),
			check: func(t *testing.T, cfg *tls.Config) {
				assert.NotNil(t, cfg.RootCAs)
				assert.Len(t, cfg.Certificates, 1)
			},
		},
		{
			name:          "client cert without CA -> insecure but still presents cert",
			objs:          []client.Object{clientSecret(map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM})},
			storageClient: clientWith(v1alpha1.StorageClientSpec{ClientCertSecret: certRef}),
			check: func(t *testing.T, cfg *tls.Config) {
				assert.True(t, cfg.InsecureSkipVerify)
				assert.Len(t, cfg.Certificates, 1)
			},
		},
		{
			name:          "malformed CA PEM",
			objs:          []client.Object{caConfigMap("ca.crt", []byte("not a pem"))},
			storageClient: clientWith(v1alpha1.StorageClientSpec{ServerCAConfigMap: caRef}),
			wantErr:       true,
		},
		{
			name:          "CA configmap missing",
			storageClient: clientWith(v1alpha1.StorageClientSpec{ServerCAConfigMap: caRef}),
			wantErr:       true,
		},
		{
			name:          "CA configmap key absent",
			objs:          []client.Object{caConfigMap("other.crt", caPEM)},
			storageClient: clientWith(v1alpha1.StorageClientSpec{ServerCAConfigMap: caRef}),
			wantErr:       true,
		},
		{
			name:          "client secret missing",
			storageClient: clientWith(v1alpha1.StorageClientSpec{ClientCertSecret: certRef}),
			wantErr:       true,
		},
		{
			name:          "client secret missing tls.key",
			objs:          []client.Object{clientSecret(map[string][]byte{"tls.crt": certPEM})},
			storageClient: clientWith(v1alpha1.StorageClientSpec{ClientCertSecret: certRef}),
			wantErr:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(tc.objs...).Build()
			cfg, err := LoadClientTLSConfig(context.Background(), k8sClient, tc.storageClient, testNamespace)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg)
			tc.check(t, cfg)
		})
	}
}
