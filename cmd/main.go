// Command manager runs cluster-api-provider-apiary's controllers: the
// Cluster API infrastructure provider for Apiary (see this repo's
// README and the apiary repo's CLAUDE.md for the surrounding
// architecture this plugs into).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	infrav1 "github.com/glenjbarber/cluster-api-provider-apiary/api/v1beta1"
	"github.com/glenjbarber/cluster-api-provider-apiary/internal/apiaryclient"
	"github.com/glenjbarber/cluster-api-provider-apiary/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)
	_ = infrav1.AddToScheme(scheme)
}

// nodeEndpoint is one entry of -apiary-node-map: an Apiary node_id's
// restshimd base URL and (optional) API key.
type nodeEndpoint struct {
	baseURL string
	apiKey  string
}

// parseNodeMap parses "node-a=http://10.50.0.14:8080:KEY1;node-b=http://10.50.0.11:8080"
// into a lookup by node_id. Modeled directly on the apiary repo's own
// cmd/frontend -role-map flag - an explicit, operator-maintained
// mapping rather than any kind of service discovery, since Apiary
// itself has none either.
func parseNodeMap(spec string) (map[string]nodeEndpoint, error) {
	nodes := make(map[string]nodeEndpoint)
	if spec == "" {
		return nodes, nil
	}
	for _, entry := range strings.Split(spec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		nodeID, rest, ok := strings.Cut(entry, "=")
		if !ok || nodeID == "" || rest == "" {
			return nil, fmt.Errorf("invalid -apiary-node-map entry %q: want node_id=base_url(:api_key)?", entry)
		}
		baseURL, apiKey, _ := strings.Cut(rest, "|")
		if _, exists := nodes[nodeID]; exists {
			return nil, fmt.Errorf("-apiary-node-map: node_id %q listed more than once", nodeID)
		}
		nodes[nodeID] = nodeEndpoint{baseURL: baseURL, apiKey: apiKey}
	}
	return nodes, nil
}

func main() {
	var metricsAddr string
	var probeAddr string
	var nodeMapSpec string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&nodeMapSpec, "apiary-node-map", "",
		`Apiary node_id to restshimd endpoint mapping, e.g. "apiarium=http://10.50.0.14:8080|APIKEY;freebsd-apiary=http://10.50.0.11:8080". `+
			`The API key half (after |) is optional, matching restshimd's own "no key configured means unauthenticated" behavior. `+
			`Falls back to the APIARY_NODE_MAP environment variable if unset - prefer that in-cluster (sourced from a Secret via `+
			`env.valueFrom.secretKeyRef, see config/manager/manager.yaml) so the API key half never appears in a Pod's own spec/args, `+
			`visible to anyone with "kubectl get pod -o yaml" access.`)
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if nodeMapSpec == "" {
		nodeMapSpec = os.Getenv("APIARY_NODE_MAP")
	}
	nodeMap, err := parseNodeMap(nodeMapSpec)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "unable to start manager:", err)
		os.Exit(1)
	}

	newApiaryClient := func(nodeID string) (*apiaryclient.Client, error) {
		ep, ok := nodeMap[nodeID]
		if !ok {
			return nil, fmt.Errorf("no -apiary-node-map entry for node_id %q", nodeID)
		}
		return apiaryclient.NewClient(ep.baseURL, ep.apiKey, nil), nil
	}

	if err := (&controller.ApiaryClusterReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		fmt.Fprintln(os.Stderr, "unable to create ApiaryCluster controller:", err)
		os.Exit(1)
	}
	if err := (&controller.ApiaryMachineReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		NewApiaryClient: newApiaryClient,
	}).SetupWithManager(mgr); err != nil {
		fmt.Fprintln(os.Stderr, "unable to create ApiaryMachine controller:", err)
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		fmt.Fprintln(os.Stderr, "unable to set up health check:", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		fmt.Fprintln(os.Stderr, "unable to set up ready check:", err)
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintln(os.Stderr, "problem running manager:", err)
		os.Exit(1)
	}
}
