/*
Copyright 2020 Ryo TAKAISHI.

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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SecurityGroupSpec defines the desired state of SecurityGroup
type SecurityGroupSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of SecurityGroup. Edit SecurityGroup_types.go to remove/update
	NodeSelector map[string]string   `json:"nodeSelector"`
	Name         string              `json:"name"`
	Tenant       string              `json:"tenant,omitempty"`
	Rules        []SecurityGroupRule `json:"rules"`
	ID           string              `json:"id,omitempty"`
}

type SecurityGroupRule struct {
	// +kubebuilder:validation:Enum=ingress;egress
	Direction string `json:"direction"`

	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	PortRangeMax int `json:"portRangeMax"`

	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	PortRangeMin   int    `json:"portRangeMin"`
	RemoteIpPrefix string `json:"remoteIpPrefix"`

	// +kubebuilder:validation:Enum=IPv4;IPv6
	EtherType string `json:"etherType"`

	// +kubebuilder:validation:Enum=ah;dccp;egp;esp;gre;icmp;igmp;ipv6-encap;ipv6-frag;ipv6-icmp;ipv6-nonxt;ipv6-opts;ipv6-route;ospf;pgm;rsvp;sctp;tcp;udp;udplite;vrrp
	Protocol string `json:"protocol"`
}

// SecurityGroupStatus defines the observed state of SecurityGroup
type SecurityGroupStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Nodes []string `json:"nodes,omitempty"`
	ID    string   `json:"id,omitempty"`
	Name  string   `json:"name,omitempty"`
}

// +kubebuilder:object:root=true

// SecurityGroup is the Schema for the securitygroups API
// +kubebuilder:subresource:status
type SecurityGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SecurityGroupSpec   `json:"spec,omitempty"`
	Status SecurityGroupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SecurityGroupList contains a list of SecurityGroup
type SecurityGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SecurityGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SecurityGroup{}, &SecurityGroupList{})
}
