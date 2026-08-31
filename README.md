# cluster-api-provider-apiary

A [Cluster API](https://cluster-api.sigs.k8s.io/) infrastructure
provider for [Apiary](https://github.com/glenjbarber/apiary), a
FreeBSD-native virtualization platform (bhyve VMs, jails, ZFS
replication, raft consensus). This is what lets a standard CAPI
management cluster provision real Linux Kubernetes nodes as bhyve VMs
on an Apiary cluster - the same approach Proxmox itself uses for
Kubernetes support (Linux VMs under the hypervisor, not native FreeBSD
jails, since `kubelet`/`containerd` need Linux primitives jails don't
provide).

Paired with the existing upstream `kubeadm` bootstrap and
control-plane providers - this repo only implements the
infrastructure half (`ApiaryCluster`/`ApiaryMachine`/
`ApiaryMachineTemplate`).

## How it works

- **`ApiaryCluster`** is close to a no-op. Apiary has no load balancer
  of its own, so there's no infrastructure to provision at the cluster
  level - the controller just marks the cluster `Ready` once a caller
  has supplied `spec.controlPlaneEndpoint`.
- **`ApiaryMachine`** does the real work, one object per Apiary VM:
  1. Waits for the owning `Machine`'s bootstrap `Secret` (kubeadm's
     rendered cloud-init user-data) to exist.
  2. Builds a [NoCloud](https://cloudinit.readthedocs.io/en/latest/reference/datasources/nocloud.html)
     seed ISO from that user-data (`internal/nocloud` - a real
     ISO9660 image, pure Go, no `genisoimage`/`mkisofs` dependency) and
     uploads it to the target Apiary node via `UploadISO`
     (`POST /v1/isos`, added to Apiary's own `restshim` as part of this
     work - see apiary's ADR-0031).
  3. Creates the VM (`POST /v1/vms`) referencing that seed ISO plus a
     pre-uploaded raw base disk image (`base_image_name` - apiary's
     ADR-0031) so the VM boots into a real Linux OS with cloud-init
     already installed, not a blank disk.
  4. Polls the VM (`GET /v1/vms/{id}`) until Apiary's own reconciler
     reports an assigned IP, then marks the `ApiaryMachine` `Ready`.
  5. On deletion, calls `DeleteVM` before removing its finalizer - never
     just deletes the Kubernetes object and leaves a real VM running.

All Apiary API access goes through `internal/apiaryclient`, a REST
client for `restshimd` mirroring
[`terraform-provider-apiary`](https://github.com/glenjbarber/terraform-provider-apiary)'s
own client pattern (bearer-token auth, JSON in/out, `ErrNotFound` for
404) - Apiary's second external REST client, kept deliberately
consistent with the first.

## Known limitations (v1)

- **No load balancer / HA control plane.** `ApiaryCluster`'s
  `controlPlaneEndpoint` is caller-supplied, not provisioned. For a
  single control-plane node, that's the node's own VM IP - which you
  need to know (or reserve) *before* `KubeadmControlPlane` will
  generate that node's bootstrap data at all, since CAPI's own contract
  requires `Cluster.spec.controlPlaneEndpoint` set first. If your
  network doesn't give you a predictable address ahead of time, create
  the control-plane `ApiaryMachine` by hand, read its
  `status.addresses` once ready, then patch `Cluster`/`ApiaryCluster`
  and apply the rest. See `examples/cluster.yaml`.
- **No automatic `providerID` → kubelet wiring.** This provider sets
  `ApiaryMachine.spec.providerID` to `apiary://<vm-id>` as soon as the
  VM is created, but nothing in Apiary or this provider can reach into
  the guest OS to set kubelet's own `--provider-id` flag to match (no
  cloud-controller-manager exists for Apiary). Without a match, CAPI's
  core Machine controller can never link the resulting `Node` back to
  its `Machine`. The VM's id is embedded verbatim in the NoCloud seed's
  `meta-data` (`instance-id`), so a `preKubeadmCommands` step can read
  it back from cloud-init's own `instance-data.json` after boot and
  write it into `/etc/default/kubelet` - see `examples/cluster.yaml`
  for the exact snippet. This is a real, manual integration point users
  must include in their own `KubeadmConfig(Template)`.
- **Base image is a hard prerequisite.** `spec.baseImageName` must name
  a raw (not qcow2), already-bootable disk image with cloud-init
  pre-installed, uploaded to the target Apiary node ahead of time via
  `UploadISO`. Apiary does zero format conversion (see apiary's
  ADR-0031/CLAUDE.md) - a plain `qemu-img convert -O raw` of an
  official Ubuntu/Debian cloud image is the expected starting point.
- **`nodeID` (and `networkID`, per-machine) are caller-set, never
  scheduled** - matching Apiary's own philosophy throughout its API
  (see apiary's CLAUDE.md): nothing decides which Apiary cluster node
  or network a `Machine` should land on beyond what you put in its
  `ApiaryMachineTemplate`.

## Configuration

The controller manager takes one provider-specific flag,
`-apiary-node-map`, mapping each Apiary `node_id` an `ApiaryMachine`
might reference to that node's `restshimd` endpoint and (optional) API
key:

```
-apiary-node-map="apiarium=http://10.50.0.14:8080|APIKEY;freebsd-apiary=http://10.50.0.11:8080"
```

This mirrors apiary's own `cmd/frontend -role-map` flag: an explicit,
operator-maintained mapping rather than any kind of service discovery,
since Apiary itself has none either.

## Building and deploying

```bash
go build -o bin/manager ./cmd
```

CRDs and RBAC are hand-generated via `controller-gen` (no `kubebuilder`
CLI dependency) into `config/crd/bases` and `config/rbac`; `config/default`
kustomizes the whole deployment (namespace, CRDs, RBAC, manager
Deployment) together:

```bash
kubectl apply -k config/default
```

To regenerate CRDs/deepcopy code after changing `api/v1beta1`:

```bash
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5 object:headerFile="" paths="./api/..."
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5 crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5 rbac:roleName=manager-role paths="./internal/controller/..." output:rbac:artifacts:config=config/rbac
```

## Testing

Controller reconcile logic is unit-tested against a
`controller-runtime` fake client plus an in-memory HTTP stand-in for
`restshimd` (`internal/controller/*_test.go`) - not `envtest`, since
this environment's sandbox can't fetch `envtest`'s real
etcd/kube-apiserver binaries; the fake-client approach still exercises
the real reconcile logic and the real `apiaryclient` HTTP code path,
just against a fake API server object tracker instead of a real one.

```bash
go build ./...
go vet ./...
go test ./...
```

Live end-to-end verification (a real `kind` management cluster,
`clusterctl init` with this provider, provisioning a real bhyve VM on
a real Apiary node) is tracked as a follow-on step once a base image
is staged - see the apiary repo's CLAUDE.md for status.
