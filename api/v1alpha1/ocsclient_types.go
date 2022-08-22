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

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ocsClientPhase string

const (
	// OcsClientInitializing represents Initializing state of OcsClient
	OcsClientInitializing ocsClientPhase = "Initializing"
	// OcsClientOnboarding represents Onboarding state of OcsClient
	OcsClientOnboarding ocsClientPhase = "Onboarding"
	// OcsClientConnected represents Onboarding state of OcsClient
	OcsClientConnected ocsClientPhase = "Connected"
	// OcsClientUpdating represents Onboarding state of OcsClient
	OcsClientUpdating ocsClientPhase = "Updating"
	// OcsClientOffboarding represents Onboarding state of OcsClient
	OcsClientOffboarding ocsClientPhase = "Offboarding"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// OcsClientSpec defines the desired state of OcsClient
type OcsClientSpec struct {
	// StorageProviderEndpoint holds info to establish connection with the storage providing cluster.
	StorageProviderEndpoint string `json:"storageProviderEndpoint"`

	// OnboardingTicket holds an identity information required for consumer to onboard.
	OnboardingTicket string `json:"onboardingTicket"`

	// RequestedCapacity Will define the desired capacity requested by a consumer cluster.
	RequestedCapacity *resource.Quantity `json:"requestedCapacity,omitempty"`
}

// OcsClientStatus defines the observed state of OcsClient
type OcsClientStatus struct {
	Phase ocsClientPhase `json:"phase,omitempty"`

	// GrantedCapacity Will report the actual capacity
	// granted to the consumer cluster by the provider cluster.
	GrantedCapacity resource.Quantity `json:"grantedCapacity,omitempty"`

	// ConsumerID will hold the identity of this cluster inside the attached provider cluster
	ConsumerID string `json:"id,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// OcsClient is the Schema for the ocsclients API
type OcsClient struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OcsClientSpec   `json:"spec,omitempty"`
	Status OcsClientStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// OcsClientList contains a list of OcsClient
type OcsClientList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OcsClient `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OcsClient{}, &OcsClientList{})
}
