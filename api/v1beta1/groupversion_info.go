// Package v1beta1 contains API Schema definitions for the
// infrastructure.cluster.x-k8s.io v1beta1 API group - the group every
// Cluster API infrastructure provider registers its own Cluster/Machine
// kinds under, per the CAPI contract.
// +kubebuilder:object:generate=true
// +groupName=infrastructure.cluster.x-k8s.io
package v1beta1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
