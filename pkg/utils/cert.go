package utils

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LoadClientTLSConfig loads the mTLS certificates configured on a StorageClient and returns a
// *tls.Config ready for the provider client. Certificates live in the given namespace.
func LoadClientTLSConfig(
	ctx context.Context,
	k8sClient client.Client,
	storageClient *v1alpha1.StorageClient,
	namespace string,
) (*tls.Config, error) {
	serverCA, clientCert, clientKey, err := loadClientCertificates(ctx, k8sClient, storageClient, namespace)
	if err != nil {
		return nil, err
	}

	// TODO: mandate mTLS by removing the both-or-neither guard and the insecure fallback below.
	if (len(clientCert) == 0) != (len(clientKey) == 0) {
		return nil, fmt.Errorf("client certificate and key must both be provided or both be empty")
	}

	var tlsConfig *tls.Config
	if len(serverCA) == 0 {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	} else {
		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(serverCA) {
			return nil, fmt.Errorf("failed to parse server CA certificate")
		}
		tlsConfig = &tls.Config{
			RootCAs:    certPool,
			ServerName: cmp.Or(storageClient.Spec.ServerSAN, defaultProviderServerSAN(namespace)),
		}
	}

	if len(clientCert) > 0 {
		cert, err := tls.X509KeyPair(clientCert, clientKey)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %v", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}

// loadClientCertificates loads mTLS certificates for a StorageClient from the given namespace.
func loadClientCertificates(
	ctx context.Context,
	k8sClient client.Client,
	storageClient *v1alpha1.StorageClient,
	namespace string,
) (serverCA, clientCert, clientKey []byte, err error) {
	if storageClient.Spec.ServerCAConfigMap.Name != "" {
		key := storageClient.Spec.ServerCAConfigMap.Key
		configMap := &corev1.ConfigMap{}
		configMap.Name = storageClient.Spec.ServerCAConfigMap.Name
		configMap.Namespace = namespace
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(configMap), configMap); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get server CA configmap %s: %w", configMap.Name, err)
		}
		if len(configMap.Data[key]) == 0 {
			return nil, nil, nil, fmt.Errorf("%s not found in server CA configmap %s", key, configMap.Name)
		}
		serverCA = []byte(configMap.Data[key])
	}

	if storageClient.Spec.ClientCertSecret.Name != "" {
		secret := &corev1.Secret{}
		secret.Name = storageClient.Spec.ClientCertSecret.Name
		secret.Namespace = namespace
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get client cert secret %s: %w", secret.Name, err)
		}
		clientCert = secret.Data["tls.crt"]
		clientKey = secret.Data["tls.key"]
		if len(clientCert) == 0 || len(clientKey) == 0 {
			return nil, nil, nil, fmt.Errorf("tls.crt or tls.key not found in client cert secret %s", secret.Name)
		}
	}

	// When the spec configures neither CA nor client cert, all returns are nil;
	// the caller decides how to proceed (e.g. fall back to an insecure connection).
	return serverCA, clientCert, clientKey, nil
}

func defaultProviderServerSAN(namespace string) string {
	return fmt.Sprintf("ocs-provider-server.%s.svc", namespace)
}
