package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// ApiaryClusterSpec defines the desired state of an ApiaryCluster - the
// CAPI infrastructure-cluster counterpart to a core Cluster resource.
//
// v1 scope has no load-balancer support (see ADR in the apiary repo's
// docs/adr for the equivalent "no scheduler" philosophy this mirrors):
// ControlPlaneEndpoint is caller-supplied, not provisioned by this
// provider. For a single control-plane node this is that node's own
// VM IP; a real HA control plane needing a floating/virtual IP is
// explicit follow-on work, not attempted here.
type ApiaryClusterSpec struct {
	// ControlPlaneEndpoint is the caller-supplied host/port the workload
	// cluster's Kubernetes API is reachable on. Apiary has no load
	// balancer of its own (v1 limitation) - for a single control-plane
	// node this is that node's own VM address once known.
	// +optional
	ControlPlaneEndpoint clusterv1.APIEndpoint `json:"controlPlaneEndpoint,omitempty"`

	// NetworkID names an Apiary NetworkDefinition (see the apiary repo's
	// ADR-0022) every ApiaryMachine in this cluster's NIC belongs to.
	// Empty means Apiary's flat, node-wide bridge behavior.
	// +optional
	NetworkID string `json:"networkID,omitempty"`
}

// ApiaryClusterStatus defines the observed state of an ApiaryCluster.
type ApiaryClusterStatus struct {
	// Ready denotes the infrastructure is ready to be used, i.e. once
	// ControlPlaneEndpoint has been set. Required by the CAPI contract -
	// Cluster's own controller waits on this before proceeding.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// FailureReason/FailureMessage mirror the CAPI contract's standard
	// terminal-failure surface, for a condition this provider can't
	// recover from on its own.
	// +optional
	FailureReason string `json:"failureReason,omitempty"`
	// +optional
	FailureMessage string `json:"failureMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=apiaryclusters,scope=Namespaced,categories=cluster-api
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1beta1"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".spec.controlPlaneEndpoint.host"

// ApiaryCluster is the Schema for the apiaryclusters API - the
// infrastructure-provider-specific resource a core Cluster references
// via infrastructureRef, per the Cluster API contract.
type ApiaryCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ApiaryClusterSpec   `json:"spec,omitempty"`
	Status ApiaryClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ApiaryClusterList contains a list of ApiaryCluster.
type ApiaryClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApiaryCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApiaryCluster{}, &ApiaryClusterList{})
}
