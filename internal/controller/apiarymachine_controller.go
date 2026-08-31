package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"

	infrav1 "github.com/glenjbarber/cluster-api-provider-apiary/api/v1beta1"
	"github.com/glenjbarber/cluster-api-provider-apiary/internal/apiaryclient"
	"github.com/glenjbarber/cluster-api-provider-apiary/internal/nocloud"
)

// ApiaryMachineFinalizer ensures Reconcile runs one more time on
// deletion, so the backing Apiary VM is actually torn down rather than
// leaking real bhyve/ZFS resources behind a deleted Kubernetes object -
// the same "never just remove the record" caution Apiary's own
// DeleteVM/PurgeVM split already applies (see the apiary repo's
// ADR-0016).
const ApiaryMachineFinalizer = "apiarymachine.infrastructure.cluster.x-k8s.io"

// notReadyRequeueInterval is how often Reconcile polls a VM's phase/IP
// while waiting for Apiary's own reconciler to finish provisioning it -
// short enough that "watch it boot" feels responsive, long enough not
// to hammer restshimd.
const notReadyRequeueInterval = 10 * time.Second

// ApiaryMachineReconciler reconciles an ApiaryMachine object - the real
// logic in this provider. One ApiaryMachine maps to exactly one Apiary
// VM: create it (seeding cloud-init bootstrap data via a NoCloud ISO
// this controller builds itself, since Apiary has none of its own -
// see internal/nocloud and the apiary repo's ADR-0031), then poll until
// Apiary's reconciler reports it ready with an assigned IP.
type ApiaryMachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// NewAPIaryClient builds the REST client for a given Apiary node's
	// restshimd base URL/API key. A func field (not a fixed Client)
	// because - unlike a single-cluster Terraform provider config -
	// different ApiaryMachines in the same management cluster can name
	// different Apiary node_ids, each reachable at its own address; see
	// this repo's README for the -apiary-node-map flag that supplies
	// the address/key lookup this func closes over.
	NewApiaryClient func(nodeID string) (*apiaryclient.Client, error)
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=apiarymachines,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=apiarymachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *ApiaryMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	apiaryMachine := &infrav1.ApiaryMachine{}
	if err := r.Get(ctx, req.NamespacedName, apiaryMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	machine, err := util.GetOwnerMachine(ctx, r.Client, apiaryMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		log.Info("Machine Controller has not yet set OwnerRef on ApiaryMachine")
		return ctrl.Result{}, nil
	}

	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}

	apiaryCluster := &infrav1.ApiaryCluster{}
	if cluster.Spec.InfrastructureRef != nil {
		key := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Spec.InfrastructureRef.Name}
		if err := r.Get(ctx, key, apiaryCluster); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	if !apiaryMachine.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, apiaryMachine)
	}

	return r.reconcileNormal(ctx, apiaryMachine, machine, apiaryCluster)
}

func (r *ApiaryMachineReconciler) reconcileNormal(ctx context.Context, apiaryMachine *infrav1.ApiaryMachine, machine *clusterv1.Machine, apiaryCluster *infrav1.ApiaryCluster) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(apiaryMachine, ApiaryMachineFinalizer) {
		controllerutil.AddFinalizer(apiaryMachine, ApiaryMachineFinalizer)
		if err := r.Update(ctx, apiaryMachine); err != nil {
			return ctrl.Result{}, err
		}
	}

	if machine.Spec.Bootstrap.DataSecretName == nil {
		return ctrl.Result{RequeueAfter: notReadyRequeueInterval}, nil
	}

	apiaryCli, err := r.NewApiaryClient(apiaryMachine.Spec.NodeID)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building apiary client for node %q: %w", apiaryMachine.Spec.NodeID, err)
	}

	if apiaryMachine.Status.VMID == "" {
		return r.createVM(ctx, apiaryMachine, machine, apiaryCluster, apiaryCli)
	}

	return r.pollVM(ctx, apiaryMachine, apiaryCli)
}

// createVM builds the NoCloud seed ISO from the Machine's bootstrap
// Secret, uploads it, and creates the backing Apiary VM. ProviderID is
// set immediately, using the VM id (known synchronously from CreateVM's
// own response - see the apiary repo's raft FSM, which allocates a VM's
// id/IP deterministically at Apply time, before anything actually
// boots). For this to let CAPI's core Machine controller match the
// resulting Node back to this Machine, the bootstrap data itself must
// configure kubelet with a matching --provider-id - see this repo's
// README for the required KubeadmConfigTemplate snippet, since Apiary
// has no cloud-provider integration of its own to do this automatically.
func (r *ApiaryMachineReconciler) createVM(ctx context.Context, apiaryMachine *infrav1.ApiaryMachine, machine *clusterv1.Machine, apiaryCluster *infrav1.ApiaryCluster, apiaryCli *apiaryclient.Client) (ctrl.Result, error) {
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Namespace: apiaryMachine.Namespace, Name: *machine.Spec.Bootstrap.DataSecretName}
	if err := r.Get(ctx, secretKey, secret); err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching bootstrap secret %s: %w", secretKey, err)
	}
	userData, ok := secret.Data["value"]
	if !ok {
		return ctrl.Result{}, fmt.Errorf("bootstrap secret %s has no %q key", secretKey, "value")
	}

	vmID := apiaryMachineVMID(apiaryMachine)
	isoName := vmID + "-seed.iso"

	seedISO, err := nocloud.BuildSeedISO(vmID, apiaryMachine.Name, userData)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building cloud-init seed iso: %w", err)
	}
	sum := sha256.Sum256(seedISO)
	if err := apiaryCli.UploadISO(ctx, isoName, hex.EncodeToString(sum[:]), bytes.NewReader(seedISO)); err != nil {
		return ctrl.Result{}, fmt.Errorf("uploading cloud-init seed iso: %w", err)
	}

	networkID := apiaryMachine.Spec.NetworkID
	if networkID == "" && apiaryCluster != nil {
		networkID = apiaryCluster.Spec.NetworkID
	}

	vm, err := apiaryCli.CreateVM(ctx, apiaryclient.VM{
		ID:            vmID,
		Name:          apiaryMachine.Name,
		VCPUs:         apiaryMachine.Spec.VCPUs,
		MemoryMB:      apiaryMachine.Spec.MemoryMB,
		NodeID:        apiaryMachine.Spec.NodeID,
		DesiredState:  "running",
		ISOName:       isoName,
		NetworkID:     networkID,
		BaseImageName: apiaryMachine.Spec.BaseImageName,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating apiary vm: %w", err)
	}

	providerID := fmt.Sprintf("apiary://%s", vm.ID)
	apiaryMachine.Spec.ProviderID = &providerID
	if err := r.Update(ctx, apiaryMachine); err != nil {
		return ctrl.Result{}, err
	}

	apiaryMachine.Status.VMID = vm.ID
	if err := r.Status().Update(ctx, apiaryMachine); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: notReadyRequeueInterval}, nil
}

// pollVM checks the backing VM's current state and marks the
// ApiaryMachine ready once Apiary's own reconciler has actually booted
// it and assigned an IP - never assumed ready just because CreateVM
// itself succeeded, since that only records the definition (see the
// apiary repo's own managerd/CLAUDE.md distinction between a VM
// definition existing and a VM actually running).
func (r *ApiaryMachineReconciler) pollVM(ctx context.Context, apiaryMachine *infrav1.ApiaryMachine, apiaryCli *apiaryclient.Client) (ctrl.Result, error) {
	vm, err := apiaryCli.GetVM(ctx, apiaryMachine.Status.VMID)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting apiary vm %s: %w", apiaryMachine.Status.VMID, err)
	}

	if vm.IPAddress == "" {
		return ctrl.Result{RequeueAfter: notReadyRequeueInterval}, nil
	}

	apiaryMachine.Status.Addresses = []clusterv1.MachineAddress{
		{Type: clusterv1.MachineInternalIP, Address: vm.IPAddress},
	}
	apiaryMachine.Status.Ready = true
	if err := r.Status().Update(ctx, apiaryMachine); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ApiaryMachineReconciler) reconcileDelete(ctx context.Context, apiaryMachine *infrav1.ApiaryMachine) (ctrl.Result, error) {
	if apiaryMachine.Status.VMID != "" {
		apiaryCli, err := r.NewApiaryClient(apiaryMachine.Spec.NodeID)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("building apiary client for node %q: %w", apiaryMachine.Spec.NodeID, err)
		}
		if err := apiaryCli.DeleteVM(ctx, apiaryMachine.Status.VMID); err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting apiary vm %s: %w", apiaryMachine.Status.VMID, err)
		}
	}

	controllerutil.RemoveFinalizer(apiaryMachine, ApiaryMachineFinalizer)
	if err := r.Update(ctx, apiaryMachine); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// apiaryMachineVMID derives a stable Apiary VM id from the
// ApiaryMachine's own namespace/name - deterministic so a controller
// restart between createVM and the first successful Status update
// (before Status.VMID was persisted) can recompute the same id on
// retry instead of creating a second, orphaned VM.
func apiaryMachineVMID(apiaryMachine *infrav1.ApiaryMachine) string {
	sum := sha256.Sum256([]byte(apiaryMachine.Namespace + "/" + apiaryMachine.Name))
	return "capi-" + hex.EncodeToString(sum[:])[:12]
}

func (r *ApiaryMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.ApiaryMachine{}).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("ApiaryMachine"))),
		).
		Complete(r)
}
