package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// ApiaryMachineSpec defines the desired state of an ApiaryMachine - the
// CAPI infrastructure-machine counterpart to a core Machine resource,
// mapping directly onto one Apiary VMDefinition.
//
// Deliberately no scheduling logic here, matching Apiary's own
// philosophy throughout its API (see the apiary repo's CLAUDE.md):
// NodeID is caller-set, never computed.
type ApiaryMachineSpec struct {
	// ProviderID is set by this controller once the backing VM exists,
	// per the CAPI contract (the core Machine controller waits on this
	// being set on the Node object it matches against).
	// +optional
	ProviderID *string `json:"providerID,omitempty"`

	// NodeID names the Apiary cluster node this VM is assigned to run
	// on - caller-set, exactly like Apiary's own VMDefinition.node_id.
	// No automatic node selection is performed.
	NodeID string `json:"nodeID,omitempty"`

	// VCPUs/MemoryMB size the VM. Zero uses Apiary's own reconciler
	// defaults (1 vCPU / 512MB).
	// +optional
	VCPUs uint32 `json:"vcpus,omitempty"`
	// +optional
	MemoryMB uint64 `json:"memoryMB,omitempty"`

	// BaseImageName names a raw disk image already uploaded to the
	// target Apiary node via UploadISO (see apiary's ADR-0031) - the
	// VM's disk is seeded from this image instead of created blank.
	// Required in practice for a Kubernetes node VM to boot into a
	// working Linux OS unattended; the field is optional at the API
	// level only because Apiary's own VMDefinition treats it the same
	// way.
	// +optional
	BaseImageName string `json:"baseImageName,omitempty"`

	// NetworkID names an Apiary NetworkDefinition this VM's NIC belongs
	// to - normally inherited from the owning ApiaryCluster's own
	// NetworkID by the controller, but overridable per-machine.
	// +optional
	NetworkID string `json:"networkID,omitempty"`

	// StaticIPAddress overrides the address this ApiaryMachine reports
	// ready at, for a VM that isn't on an Apiary-managed
	// NetworkDefinition (NetworkID unset) - a flat-bridge VM gets its
	// IP from a real, external DHCP server, which Apiary's own
	// VMDefinition.ip_address never tracks, so the controller would
	// otherwise wait for an IP that will never appear in Apiary's own
	// API. Set this when the operator has externally guaranteed the
	// address (e.g. a static DHCP reservation on their own router, keyed
	// by this VM's own deterministic MAC - see the apiary repo's
	// ADR-0044) - the controller trusts it as-is, with no way to verify
	// the VM actually received it.
	// +optional
	StaticIPAddress string `json:"staticIPAddress,omitempty"`
}

// ApiaryMachineStatus defines the observed state of an ApiaryMachine.
type ApiaryMachineStatus struct {
	// Ready is true once the backing Apiary VM has reached phase "ready"
	// and been assigned an IP address - required by the CAPI contract.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// Addresses mirrors the CAPI contract's machine address surface,
	// populated from the VM's Apiary-assigned IP once known.
	// +optional
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`

	// VMID is the Apiary VM id this ApiaryMachine created, once known -
	// distinct from the Kubernetes object's own name so the two id
	// spaces (Apiary's, Kubernetes') are never conflated.
	// +optional
	VMID string `json:"vmID,omitempty"`

	// FailureReason/FailureMessage mirror the CAPI contract's standard
	// terminal-failure surface.
	// +optional
	FailureReason string `json:"failureReason,omitempty"`
	// +optional
	FailureMessage string `json:"failureMessage,omitempty"`

	// Conditions implements the CAPI contract's condition surface,
	// consumed by clusterctl describe and the core controllers' own
	// readiness checks.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

// GetConditions/SetConditions satisfy clusterv1.Setter, required for
// this type to work with CAPI's own conditions helper package.
func (m *ApiaryMachine) GetConditions() clusterv1.Conditions {
	return m.Status.Conditions
}

func (m *ApiaryMachine) SetConditions(conditions clusterv1.Conditions) {
	m.Status.Conditions = conditions
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=apiarymachines,scope=Namespaced,categories=cluster-api
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1beta1"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="VMID",type="string",JSONPath=".status.vmID"
// +kubebuilder:printcolumn:name="ProviderID",type="string",JSONPath=".spec.providerID"

// ApiaryMachine is the Schema for the apiarymachines API.
type ApiaryMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ApiaryMachineSpec   `json:"spec,omitempty"`
	Status ApiaryMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ApiaryMachineList contains a list of ApiaryMachine.
type ApiaryMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApiaryMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApiaryMachine{}, &ApiaryMachineList{})
}
