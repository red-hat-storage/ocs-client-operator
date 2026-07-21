/*
Copyright 2026 Red Hat OpenShift Data Foundation.
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
	"crypto/tls"

	ocstlsv1 "github.com/red-hat-storage/ocs-tls-profiles/api/v1"
)

func BuildServerTLSOpts(profile *ocstlsv1.TLSProfile, domain, server string) (*tls.Config, error) {
	if profile == nil {
		return nil, nil
	}
	tlsConfig, exist := ocstlsv1.GetConfigForServer(profile, domain, server)
	if !exist {
		return nil, nil
	}
	if err := ocstlsv1.ValidateTLSConfig(tlsConfig); err != nil {
		return nil, err
	}
	return ocstlsv1.GetGoTLSConfig(tlsConfig), nil
}
