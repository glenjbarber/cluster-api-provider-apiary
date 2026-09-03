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
  need to know (or reserve) *before* `KubeadmControlPlane` (or a plain
  `KubeadmConfig`, for a manually-managed control-plane `Machine` like
  this repo's own real verification used) will generate that node's
  bootstrap data at all, since CAPI's own contract requires
  `Cluster.spec.controlPlaneEndpoint` set first. **Live-verified real
  solution**, if your network doesn't hand out predictable addresses:
  `ApiaryMachineSpec.StaticIPAddress` plus a static DHCP reservation
  keyed by the VM's own deterministic MAC. The VM id this provider will
  create is itself deterministic - a SHA-256 hash of the owning
  `ApiaryMachine`'s own `namespace/name` (`apiaryMachineVMID`, prefixed
  `capi-`) - so you can compute both the future VM id *and* its MAC
  (apiary's own `deriveMAC`, see its ADR-0044) before creating anything
  at all, reserve a real static lease for that MAC on your own router
  ahead of time, and set `controlPlaneEndpoint`/`StaticIPAddress` to
  that pre-reserved address from the very first `kubectl apply` -
  closing the chicken-and-egg gap entirely, no "create by hand, patch
  afterward" dance needed. (`status.addresses`/patch-afterward, as this
  section used to suggest, remains a fallback for a network that gives
  you no way to reserve an address at all.)
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

The controller manager takes one provider-specific setting, mapping
each Apiary `node_id` an `ApiaryMachine` might reference to that node's
`restshimd` endpoint and (optional) API key:

```
apiarium=http://10.50.0.14:8080|APIKEY;freebsd-apiary=http://10.50.0.11:8080
```

This mirrors apiary's own `cmd/frontend -role-map` flag: an explicit,
operator-maintained mapping rather than any kind of service discovery,
since Apiary itself has none either.

Two ways to supply it:

- **`-apiary-node-map` flag** - fine for local, out-of-cluster runs
  (`go run ./cmd -apiary-node-map=...`), but never use this in a real
  Deployment: flag values live in the Pod's own spec, readable by
  anyone with `kubectl get pod -o yaml` access - and the value half of
  this particular flag carries real API keys.
- **`APIARY_NODE_MAP` environment variable** - checked whenever the
  flag is empty. `config/manager/manager.yaml` wires this to a Secret
  (`apiary-credentials`, key `node-map`) via `env.valueFrom.secretKeyRef`,
  so the real value never appears in the Deployment manifest itself.
  Create it once per cluster before installing this provider:

  ```bash
  kubectl create namespace cluster-api-provider-apiary-system
  kubectl create secret generic apiary-credentials \
    --namespace cluster-api-provider-apiary-system \
    --from-literal=node-map='apiarium=http://10.50.0.14:8080|APIKEY;freebsd-apiary=http://10.50.0.11:8080'
  ```

  The Secret reference is marked `optional: true`, so the manifest
  still applies cleanly without it - every `ApiaryMachine` reconcile
  then just fails fast with a clear "no `-apiary-node-map` entry" error
  until the Secret exists, rather than the Pod refusing to start.

## Installing via clusterctl

Released versions publish a `ghcr.io/glenjbarber/cluster-api-provider-apiary`
image plus GitHub Release assets (`infrastructure-components.yaml`,
`metadata.yaml`) that `clusterctl` itself expects. Add this repository
as a custom provider in `~/.cluster-api/clusterctl.yaml`:

```yaml
providers:
  - name: apiary
    url: https://github.com/glenjbarber/cluster-api-provider-apiary/releases/latest/infrastructure-components.yaml
    type: InfrastructureProvider
```

then, after creating the `apiary-credentials` Secret above (clusterctl
installs the namespace itself, so create the Secret only after `init`,
or `kubectl create namespace` first as shown above and create it
before):

```bash
clusterctl init --infrastructure apiary
```

## Building and deploying (development)

```bash
go build -o bin/manager ./cmd
```

CRDs and RBAC are hand-generated via `controller-gen` (no `kubebuilder`
CLI dependency) into `config/crd/bases` and `config/rbac`; `config/default`
kustomizes the whole deployment (namespace, CRDs, RBAC, manager
Deployment) together. For a quick local-cluster test with an
already-built image (skips clusterctl/GHCR entirely):

```bash
make docker-build IMG=apiary-capi-manager:dev
kind load docker-image apiary-capi-manager:dev --name <your kind cluster>
cd config/manager && go run sigs.k8s.io/kustomize/kustomize/v5@v5.8.1 edit set image controller=apiary-capi-manager:dev
kubectl apply -k config/default
```

To regenerate CRDs/deepcopy code after changing `api/v1beta1`:

```bash
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5 object:headerFile="" paths="./api/..."
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5 crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5 rbac:roleName=manager-role paths="./internal/controller/..." output:rbac:artifacts:config=config/rbac
```

## Releasing

Pushing a tag matching `v*.*.*` (e.g. `v0.1.0`) triggers
`.github/workflows/release.yml`, which builds/tests the tagged commit,
builds and pushes a multi-arch (`linux/amd64`/`linux/arm64`) image to
GHCR tagged with both the version and `latest`, renders
`infrastructure-components.yaml` via `make build-installer`, and
publishes a GitHub Release carrying that file plus `metadata.yaml` -
the two assets clusterctl's own provider-repository convention expects.
`metadata.yaml`'s `releaseSeries` must be kept in sync with the
contract version (`v1beta1`, matching the Cluster API version this
provider was built against) whenever that changes.

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

### Live verification (done)

Verified against a real `kind` management cluster (`clusterctl init`
with the upstream core/kubeadm-bootstrap/kubeadm-control-plane
providers, this provider's own manager run locally via `go run ./cmd`
against the kind kubeconfig rather than deployed as an in-cluster
image) and a real Apiary node (`apiarium`, via an SSH-tunneled
`restshimd`). A real Ubuntu 24.04 cloud image was staged as a base
image (`qemu-img convert -O raw`, uploaded via `UploadISO`), and a
`Machine`+`ApiaryMachine` created directly (bypassing the kubeadm
bootstrap provider - static `bootstrap.dataSecretName`, a documented,
supported CAPI pattern - to isolate this provider's own logic from the
bootstrap-provider chicken-and-egg problem described above).

Confirmed: the NoCloud seed ISO was built, hash-verified, and
uploaded; the VM was created with the base image attached; a real
`bhyve` process booted continuously from the seeded disk (previously,
before two bugs this pass found and fixed, the same VM failed
immediately at UEFI boot with "no bootable option or device was
found" against a blank disk). Two real bugs were found and fixed along
the way:

- A stale `managerd`/`raftd` deployment on the test node hadn't picked
  up `base_image_name` at all (a deployment/rebuild issue on the
  Apiary side, not a code bug in this repo, but the fix required
  rebuilding and redeploying both binaries on both nodes of a live
  2-node raft cluster to restore consistency).
- `createVM`'s deterministic-id scheme raced against
  `controller-runtime`'s cache: a `Reconcile` starting from a stale
  read of `Status.VMID == ""` (before an earlier successful
  `createVM`'s write had propagated to the cache) retried `CreateVM`
  and got `"already exists"` back. Fixed by adopting the existing VM
  (`GetVM`) instead of treating that as a real failure - see
  `TestApiaryMachineReconciler_CreateVM_AdoptsAlreadyExistingVM`.

### Follow-up: fully confirmed live, end to end

The gap named above (no flat bridge configured) was closed on the
Apiary side: `apiarium`'s `re0` was migrated onto a real bridge
(`bridge12`, inheriting `re0`'s own MAC so the LAN's existing DHCP
reservation for the host kept working), and `-bhyve-bridge bridge12`
is now a live `managerd` flag there. Re-running the test (letting the
VM use the flat-bridge path, no `network_id`) confirmed **real,
working networking end-to-end**: a genuine DHCPv4 lease from the LAN's
own router, pingable from an entirely separate machine across the
bridge.

That still left one open question: whether cloud-init actually
consumed the NoCloud seed data. The booted VM kept the stock image's
default hostname and never opened port 22 - not a key mismatch,
nothing was listening on port 22 at all. Diagnosing it needed to see
what the guest actually printed during boot, which required a new
Apiary-side feature (bhyve serial console log capture,
[ADR-0032](../apiary/docs/adr/0032-bhyve-serial-console-log.md) in the
apiary repo) - this base image redirects console output to serial, so
the noVNC framebuffer console showed nothing informative either way.

With the serial log actually working, the cause was visible
immediately: cloud-init's own log showed

```
DataSourceNoCloud.py[WARNING]: device /dev/sr0 with label=cidata not a valid seed.
```

`internal/nocloud`'s `BuildSeedISO` finalized its ISO9660 filesystem
with no Rock Ridge extensions - plain ISO9660's 8.3 naming rules
silently mangled `user-data`/`meta-data` into `USER_DATA`/`META_DATA`
(uppercase, hyphen stripped), which cloud-init's datasource check
rejects outright (it requires the exact, case-sensitive, hyphenated
names). **Fixed** by setting `RockRidge: true` in `go-diskfs`'s
`FinalizeOptions` - confirmed via `strings`/mounting the finalized ISO
that the real lowercase filenames are present in the raw bytes
(macOS's own ISO9660 mounter doesn't fully honor Rock Ridge for
display, which is why an initial local check looked like it hadn't
worked - the bytes were correct all along).

Re-running the full test with the fix confirmed everything, live:

```
$ ssh -i ~/.ssh/id_ed25519_apiary ubuntu@10.50.0.114 "hostname; cat /etc/motd; cloud-init status"
capi-test-cp
CLOUD_INIT_RAN_OK
status: done
```

A real bhyve VM, seeded from a real base image, booted, took its
NoCloud seed's custom hostname, ran a custom `runcmd`, and accepted
SSH via a key that only the seed data could have installed. This is
the first time this provider's full path - seed ISO build → upload →
VM create → real network → real cloud-init completion - has been
proven end to end.

**Not yet built**: a web UI page/RPC for remote serial-log viewing
(an operator reads the log file directly on the Apiary node for now),
and full multi-node kubeadm cluster joining (this pass used a static
`bootstrap.dataSecretName`, bypassing the kubeadm bootstrap provider
entirely, specifically to isolate this provider's own infrastructure
logic from the bootstrap-provider chicken-and-egg problem described
above) - both real, disclosed follow-on work.
