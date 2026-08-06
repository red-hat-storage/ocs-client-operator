package console

import (
	"testing"

	ocstlsv1 "github.com/red-hat-storage/ocs-tls-profiles/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestGetNginxProxyConf(t *testing.T) {
	tests := []struct {
		name             string
		ossl             *ocstlsv1.OpenSSLConfig
		expectedIncludes []string
		expectedExcludes []string
	}{
		{
			name:             "nil config produces no proxy TLS directives",
			ossl:             nil,
			expectedExcludes: []string{"proxy_ssl_protocols", "proxy_ssl_ciphers", "proxy_ssl_conf_command"},
		},
		{
			name: "TLS 1.3 uses proxy_ssl_conf_command Ciphersuites",
			ossl: &ocstlsv1.OpenSSLConfig{
				Protocol: "TLSv1.3",
				Ciphers:  []string{"TLS_AES_128_GCM_SHA256"},
				Groups:   []string{"X25519MLKEM768"},
			},
			expectedIncludes: []string{
				"proxy_ssl_protocols TLSv1.3;",
				"proxy_ssl_conf_command Ciphersuites TLS_AES_128_GCM_SHA256;",
				"proxy_ssl_conf_command Groups X25519MLKEM768;",
			},
			expectedExcludes: []string{
				"proxy_ssl_ciphers",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GetNginxProxyConf("client-1", "noobaaS3", "https://s3.example.com", "s3.example.com", "/etc/pki/tls/certs/ca-bundle.crt", tt.ossl)
			assert.NoError(t, err)
			for _, expected := range tt.expectedIncludes {
				assert.Contains(t, result, expected)
			}
			for _, excluded := range tt.expectedExcludes {
				assert.NotContains(t, result, excluded)
			}
		})
	}
}

func TestGenerateNginxRootConf(t *testing.T) {
	tests := []struct {
		name             string
		ossl             *ocstlsv1.OpenSSLConfig
		expectedIncludes []string
		expectedExcludes []string
	}{
		{
			name: "nil config produces no TLS directives",
			ossl: nil,
			expectedIncludes: []string{
				"ssl_certificate_key /var/serving-cert/tls.key;",
				"listen       9001 ssl;",
			},
			expectedExcludes: []string{
				"ssl_protocols",
				"ssl_ciphers",
				"ssl_conf_command",
			},
		},
		{
			name: "TLS 1.3 uses ssl_conf_command Ciphersuites",
			ossl: &ocstlsv1.OpenSSLConfig{
				Protocol: "TLSv1.3",
				Ciphers:  []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384"},
				Groups:   []string{"X25519MLKEM768", "prime256v1"},
			},
			expectedIncludes: []string{
				"ssl_protocols TLSv1.3;",
				"ssl_conf_command Ciphersuites TLS_AES_128_GCM_SHA256:TLS_AES_256_GCM_SHA384;",
				"ssl_conf_command Groups X25519MLKEM768:prime256v1;",
				"ssl_certificate_key /var/serving-cert/tls.key;",
			},
			expectedExcludes: []string{
				"ssl_ciphers",
			},
		},
		{
			name: "TLS 1.2 with ciphers uses ssl_ciphers",
			ossl: &ocstlsv1.OpenSSLConfig{
				Protocol: "TLSv1.2",
				Ciphers:  []string{"ECDHE-RSA-AES128-GCM-SHA256"},
			},
			expectedIncludes: []string{
				"ssl_protocols TLSv1.2;",
				"ssl_ciphers ECDHE-RSA-AES128-GCM-SHA256;",
			},
			expectedExcludes: []string{
				"ssl_conf_command Ciphersuites",
				"ssl_conf_command Groups",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateNginxRootConf(tt.ossl)
			for _, expected := range tt.expectedIncludes {
				assert.Contains(t, result, expected)
			}
			for _, excluded := range tt.expectedExcludes {
				assert.NotContains(t, result, excluded)
			}
		})
	}
}
