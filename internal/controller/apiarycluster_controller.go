package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"

	infrav1 "github.com/glenjbarber/cluster-api-provider-apiary/api/v1beta1"
)

// ApiaryClusterFinalizer is applied to every ApiaryCluster so its
// Reconcile has a chance to run one last time on deletion. Apiary has
// no cluster-scoped physical resource to tear down here (no load
// balancer, no shared network this provider itself created) - the
// finalizer exists for symmetry with ApiaryMachine's own finalizer and
// as a place to hang future cluster-level teardown, not because there
// is real work to do today.
const ApiaryClusterFinalizer = "apiarycluster.infrastructure.cluster.x-k8s.io"

// ApiaryClusterReconciler reconciles an ApiaryCluster object.
//
// This is deliberately near-trivial (see this repo's README/plan
// notes): Apiary has no load balancer of its own, so there is no
// infrastructure for this controller to provision. Its only real job
// is satisfying the CAPI contract - setting Status.Ready once
// Spec.ControlPlaneEndpoint has a value the caller (or, for a single
// control-plane node, a human copying that node's VM IP in by hand)
// has supplied.
type ApiaryClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=apiaryclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=apiaryclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch

func (r *ApiaryClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	apiaryCluster := &infrav1.ApiaryCluster{}
	if err := r.Get(ctx, req.NamespacedName, apiaryCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	cluster, err := util.GetOwnerCluster(ctx, r.Client, apiaryCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("Cluster Controller has not yet set OwnerRef on ApiaryCluster")
		return ctrl.Result{}, nil
	}

	if !apiaryCluster.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, apiaryCluster)
	}

	return r.reconcileNormal(ctx, apiaryCluster)
}

func (r *ApiaryClusterReconciler) reconcileNormal(ctx context.Context, apiaryCluster *infrav1.ApiaryCluster) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(apiaryCluster, ApiaryClusterFinalizer) {
		controllerutil.AddFinalizer(apiaryCluster, ApiaryClusterFinalizer)
		if err := r.Update(ctx, apiaryCluster); err != nil {
			return ctrl.Result{}, err
		}
	}

	// No load balancer to provision (v1 limitation - see this repo's
	// README) - readiness is purely a function of the caller having
	// supplied a ControlPlaneEndpoint. For a single control-plane node,
	// that's the node's own VM IP, known only once its ApiaryMachine has
	// been created and reconciled - a human (or a follow-on automation
	// piece, not built here) is expected to patch it in.
	apiaryCluster.Status.Ready = apiaryCluster.Spec.ControlPlaneEndpoint.Host != ""

	if err := r.Status().Update(ctx, apiaryCluster); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ApiaryClusterReconciler) reconcileDelete(ctx context.Context, apiaryCluster *infrav1.ApiaryCluster) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(apiaryCluster, ApiaryClusterFinalizer)
	if err := r.Update(ctx, apiaryCluster); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ApiaryClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.ApiaryCluster{}).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(
				util.ClusterToInfrastructureMapFunc(
					context.Background(),
					infrav1.GroupVersion.WithKind("ApiaryCluster"),
					mgr.GetClient(),
					&infrav1.ApiaryCluster{},
				),
			),
		).
		Complete(r)
}
