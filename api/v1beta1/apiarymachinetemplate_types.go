package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ApiaryMachineTemplateResource describes the data needed to create an
// ApiaryMachine from a template, per the CAPI contract's own
// InfrastructureMachineTemplate shape.
type ApiaryMachineTemplateResource struct {
	Spec ApiaryMachineSpec `json:"spec"`
}

// ApiaryMachineTemplateSpec defines the desired state of an
// ApiaryMachineTemplate.
type ApiaryMachineTemplateSpec struct {
	Template ApiaryMachineTemplateResource `json:"template"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=apiarymachinetemplates,scope=Namespaced,categories=cluster-api

// ApiaryMachineTemplate is the Schema for the apiarymachinetemplates
// API - referenced by a KubeadmControlPlane or MachineDeployment's
// infrastructureRef, per the Cluster API contract.
type ApiaryMachineTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ApiaryMachineTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ApiaryMachineTemplateList contains a list of ApiaryMachineTemplate.
type ApiaryMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApiaryMachineTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApiaryMachineTemplate{}, &ApiaryMachineTemplateList{})
}
