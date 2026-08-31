package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	infrav1 "github.com/glenjbarber/cluster-api-provider-apiary/api/v1beta1"
	"github.com/glenjbarber/cluster-api-provider-apiary/internal/apiaryclient"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := infrav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// fakeApiaryServer is a minimal in-memory stand-in for restshimd,
// exercising the real apiaryclient.Client HTTP code path (marshaling,
// headers, JSON decoding) without a real Apiary cluster. Records every
// VM create/get/delete and ISO upload it receives so tests can assert
// on them.
type fakeApiaryServer struct {
	mu sync.Mutex

	vms          map[string]apiaryclient.VM
	uploadedISOs []string
	deletedVMs   []string

	// ipOnGet, if set, is the IPAddress GetVM reports - simulating
	// Apiary's own reconciler having assigned one once the VM actually
	// booted (CreateVM's own response never carries one for a
	// network-less test VM).
	ipOnGet string
}

func newFakeApiaryServer() *fakeApiaryServer {
	return &fakeApiaryServer{vms: map[string]apiaryclient.VM{}}
}

func (f *fakeApiaryServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/isos":
			mr, err := r.MultipartReader()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var name string
			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if part.FormName() == "file" {
					name = part.FileName()
					io.Copy(io.Discard, part)
				}
			}
			f.uploadedISOs = append(f.uploadedISOs, name)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(apiaryclient.UploadISOResult{Name: name})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms":
			var vm apiaryclient.VM
			json.NewDecoder(r.Body).Decode(&vm)
			f.vms[vm.ID] = vm
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(vm)

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/vms/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/vms/")
			vm, ok := f.vms[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
				return
			}
			vm.IPAddress = f.ipOnGet
			json.NewEncoder(w).Encode(vm)

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/vms/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/vms/")
			delete(f.vms, id)
			f.deletedVMs = append(f.deletedVMs, id)
			json.NewEncoder(w).Encode(apiaryclient.VM{ID: id})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// setupMachineFixtures creates a Cluster, an ApiaryCluster, a Machine
// (owning apiaryMachine, labeled for the Cluster), a bootstrap Secret,
// and the ApiaryMachine itself, wired together exactly the way CAPI's
// own core controllers would - so ApiaryMachineReconciler's
// util.GetOwnerMachine/GetClusterFromMetadata calls succeed the same
// way they would against a real management cluster.
func setupMachineFixtures(t *testing.T, apiaryMachine *infrav1.ApiaryMachine) client.Client {
	t.Helper()
	const namespace = "default"

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: namespace},
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: &corev1.ObjectReference{Name: "test-cluster", Namespace: namespace},
		},
	}
	apiaryCluster := &infrav1.ApiaryCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: namespace},
		Spec:       infrav1.ApiaryClusterSpec{NetworkID: "net-1"},
	}
	dataSecretName := "test-machine-bootstrap"
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-machine",
			Namespace: namespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "test-cluster"},
		},
		Spec: clusterv1.MachineSpec{
			Bootstrap: clusterv1.Bootstrap{DataSecretName: &dataSecretName},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: dataSecretName, Namespace: namespace},
		Data:       map[string][]byte{"value": []byte("#cloud-config\nhostname: test-machine\n")},
	}

	apiaryMachine.Namespace = namespace
	apiaryMachine.OwnerReferences = append(apiaryMachine.OwnerReferences, metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Machine",
		Name:       machine.Name,
		UID:        machine.UID,
	})

	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&infrav1.ApiaryMachine{}, &infrav1.ApiaryCluster{}).
		WithObjects(cluster, apiaryCluster, machine, secret, apiaryMachine).
		Build()
}

func TestApiaryMachineReconciler_CreateVM_UploadsSeedISOAndCreatesVM(t *testing.T) {
	apiaryMachine := &infrav1.ApiaryMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "test-machine"},
		Spec:       infrav1.ApiaryMachineSpec{NodeID: "node-a", VCPUs: 2, MemoryMB: 2048, BaseImageName: "ubuntu-cloud.raw"},
	}
	c := setupMachineFixtures(t, apiaryMachine)

	server := newFakeApiaryServer()
	ts := httptest.NewServer(server.handler())
	defer ts.Close()

	r := &ApiaryMachineReconciler{
		Client: c,
		NewApiaryClient: func(nodeID string) (*apiaryclient.Client, error) {
			return apiaryclient.NewClient(ts.URL, "", nil), nil
		},
	}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-machine"}})
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.uploadedISOs) != 1 {
		t.Fatalf("uploaded ISOs = %v, want exactly one seed ISO", server.uploadedISOs)
	}
	if len(server.vms) != 1 {
		t.Fatalf("created VMs = %v, want exactly one", server.vms)
	}
	var vm apiaryclient.VM
	for _, v := range server.vms {
		vm = v
	}
	if vm.NodeID != "node-a" || vm.VCPUs != 2 || vm.MemoryMB != 2048 || vm.BaseImageName != "ubuntu-cloud.raw" {
		t.Errorf("created VM = %+v, want NodeID=node-a VCPUs=2 MemoryMB=2048 BaseImageName=ubuntu-cloud.raw", vm)
	}
	if vm.NetworkID != "net-1" {
		t.Errorf("created VM NetworkID = %q, want %q inherited from ApiaryCluster", vm.NetworkID, "net-1")
	}
	if vm.ISOName == "" {
		t.Error("created VM has no ISOName set - the seed ISO was never attached")
	}

	updated := &infrav1.ApiaryMachine{}
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "test-machine"}, updated); err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if updated.Status.VMID == "" {
		t.Error("Status.VMID not set after createVM")
	}
	if updated.Spec.ProviderID == nil || *updated.Spec.ProviderID == "" {
		t.Error("Spec.ProviderID not set after createVM")
	}
	if updated.Status.Ready {
		t.Error("Status.Ready = true immediately after createVM, want false (no IP yet)")
	}
}

func TestApiaryMachineReconciler_PollVM_BecomesReadyOnceIPAssigned(t *testing.T) {
	apiaryMachine := &infrav1.ApiaryMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "test-machine"},
		Spec:       infrav1.ApiaryMachineSpec{NodeID: "node-a"},
		Status:     infrav1.ApiaryMachineStatus{VMID: "capi-existingvm"},
	}
	c := setupMachineFixtures(t, apiaryMachine)

	server := newFakeApiaryServer()
	server.vms["capi-existingvm"] = apiaryclient.VM{ID: "capi-existingvm", NodeID: "node-a"}
	server.ipOnGet = "10.50.0.99"
	ts := httptest.NewServer(server.handler())
	defer ts.Close()

	r := &ApiaryMachineReconciler{
		Client: c,
		NewApiaryClient: func(nodeID string) (*apiaryclient.Client, error) {
			return apiaryclient.NewClient(ts.URL, "", nil), nil
		},
	}

	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-machine"}}); err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}

	updated := &infrav1.ApiaryMachine{}
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "test-machine"}, updated); err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if !updated.Status.Ready {
		t.Fatal("Status.Ready = false, want true once the VM has an IP")
	}
	if len(updated.Status.Addresses) != 1 || updated.Status.Addresses[0].Address != "10.50.0.99" {
		t.Errorf("Status.Addresses = %+v, want one InternalIP 10.50.0.99", updated.Status.Addresses)
	}
}

func TestApiaryMachineReconciler_PollVM_NotReadyWithoutIPYet(t *testing.T) {
	apiaryMachine := &infrav1.ApiaryMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "test-machine"},
		Spec:       infrav1.ApiaryMachineSpec{NodeID: "node-a"},
		Status:     infrav1.ApiaryMachineStatus{VMID: "capi-existingvm"},
	}
	c := setupMachineFixtures(t, apiaryMachine)

	server := newFakeApiaryServer()
	server.vms["capi-existingvm"] = apiaryclient.VM{ID: "capi-existingvm", NodeID: "node-a"}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()

	r := &ApiaryMachineReconciler{
		Client: c,
		NewApiaryClient: func(nodeID string) (*apiaryclient.Client, error) {
			return apiaryclient.NewClient(ts.URL, "", nil), nil
		},
	}

	res, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-machine"}})
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a RequeueAfter while waiting for an IP, got none")
	}

	updated := &infrav1.ApiaryMachine{}
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "test-machine"}, updated); err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if updated.Status.Ready {
		t.Error("Status.Ready = true, want false - VM has no IP yet")
	}
}

func TestApiaryMachineReconciler_Delete_TearsDownVMAndRemovesFinalizer(t *testing.T) {
	apiaryMachine := &infrav1.ApiaryMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-machine",
			Finalizers: []string{ApiaryMachineFinalizer},
		},
		Spec:   infrav1.ApiaryMachineSpec{NodeID: "node-a"},
		Status: infrav1.ApiaryMachineStatus{VMID: "capi-existingvm"},
	}
	c := setupMachineFixtures(t, apiaryMachine)

	server := newFakeApiaryServer()
	server.vms["capi-existingvm"] = apiaryclient.VM{ID: "capi-existingvm", NodeID: "node-a"}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()

	key := types.NamespacedName{Namespace: "default", Name: "test-machine"}
	toDelete := &infrav1.ApiaryMachine{}
	if err := c.Get(t.Context(), key, toDelete); err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if err := c.Delete(t.Context(), toDelete); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	r := &ApiaryMachineReconciler{
		Client: c,
		NewApiaryClient: func(nodeID string) (*apiaryclient.Client, error) {
			return apiaryclient.NewClient(ts.URL, "", nil), nil
		},
	}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}

	server.mu.Lock()
	deleted := server.deletedVMs
	server.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "capi-existingvm" {
		t.Errorf("deleted VMs = %v, want exactly [capi-existingvm]", deleted)
	}

	remaining := &infrav1.ApiaryMachine{}
	err := c.Get(t.Context(), key, remaining)
	if err == nil {
		t.Errorf("ApiaryMachine still exists after its last finalizer was removed: %+v", remaining)
	}
}
