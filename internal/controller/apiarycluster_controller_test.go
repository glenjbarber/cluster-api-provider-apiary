package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	infrav1 "github.com/glenjbarber/cluster-api-provider-apiary/api/v1beta1"
)

func TestApiaryClusterReconciler_NotReadyWithoutControlPlaneEndpoint(t *testing.T) {
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"}}
	apiaryCluster := &infrav1.ApiaryCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Cluster",
				Name:       "test-cluster",
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&infrav1.ApiaryCluster{}).
		WithObjects(cluster, apiaryCluster).
		Build()

	r := &ApiaryClusterReconciler{Client: c}
	key := types.NamespacedName{Namespace: "default", Name: "test-cluster"}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}

	updated := &infrav1.ApiaryCluster{}
	if err := c.Get(t.Context(), key, updated); err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if updated.Status.Ready {
		t.Error("Status.Ready = true, want false - no ControlPlaneEndpoint set yet")
	}
	if len(updated.Finalizers) == 0 {
		t.Error("expected ApiaryClusterFinalizer to be added")
	}
}

func TestApiaryClusterReconciler_ReadyOnceControlPlaneEndpointSet(t *testing.T) {
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"}}
	apiaryCluster := &infrav1.ApiaryCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Cluster",
				Name:       "test-cluster",
			}},
		},
		Spec: infrav1.ApiaryClusterSpec{
			ControlPlaneEndpoint: clusterv1.APIEndpoint{Host: "10.50.0.20", Port: 6443},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&infrav1.ApiaryCluster{}).
		WithObjects(cluster, apiaryCluster).
		Build()

	r := &ApiaryClusterReconciler{Client: c}
	key := types.NamespacedName{Namespace: "default", Name: "test-cluster"}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}

	updated := &infrav1.ApiaryCluster{}
	if err := c.Get(t.Context(), key, updated); err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if !updated.Status.Ready {
		t.Error("Status.Ready = false, want true once ControlPlaneEndpoint.Host is set")
	}
}

func TestApiaryClusterReconciler_NoOwnerClusterYetIsNotAnError(t *testing.T) {
	apiaryCluster := &infrav1.ApiaryCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
	}

	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&infrav1.ApiaryCluster{}).
		WithObjects(apiaryCluster).
		Build()

	r := &ApiaryClusterReconciler{Client: c}
	key := types.NamespacedName{Namespace: "default", Name: "test-cluster"}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile() error: %v, want nil (waiting for Cluster's own controller to set the owner ref)", err)
	}
}
