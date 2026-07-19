# nanokube

Single-node Kubernetes runtime for bootc-based hosts. nanokube wraps
upstream kubeadm phases behind a small CLI (`init`, `config`, …) and
expects the kubelet, CRI, and Kubernetes binaries to be supplied by the
bootc image rather than installed at runtime.

## Configuration

nanokube reads a multi-document YAML stream modelled on `kubeadm init
--config`. One `NanoKubeConfig` wrapper document identifies the file as
nanokube's; the rest are standard kubeadm documents (`InitConfiguration`,
`ClusterConfiguration`, optionally `KubeletConfiguration`) that nanokube
hands directly to kubeadm phases at runtime.

```yaml
apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
metadata:
  name: local
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: 10.0.0.1
nodeRegistration:
  criSocket: unix:///var/run/crio/crio.sock
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
kubernetesVersion: v1.35.0
networking:
  serviceSubnet: 10.96.0.0/12
  podSubnet: 10.244.0.0/16
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
cgroupDriver: systemd
```

`nanokube config print-defaults` emits a complete starter template
suitable for `/etc/nanokube/config.yaml`; edit
`localAPIEndpoint.advertiseAddress` to a routable IP before feeding the
file to `nanokube init`.

### kubeadm API version support

Parsing of the kubeadm portion goes through kubeadm's own
`BytesToInitConfiguration` helper, so the supported set of
`kubeadm.k8s.io/...` API versions tracks kubeadm itself: typically the
current version (`v1beta4` today) plus one deprecated predecessor
(`v1beta3`), with a `klog.Warningf` on stderr for the deprecated one.
When kubeadm drops support for an older version the corresponding
nanokube image will stop accepting configs that still use it; the
warning is the signal to migrate.

### Pinned and overridden fields

A few `ClusterConfiguration` fields are managed by the bootc image rather
than by configuration:

- `kubernetesVersion` — must equal the version pinned in this image (or
  be left unset). nanokube rejects configs that request a different
  version.
- `certificatesDir` — fixed at `/etc/kubernetes/pki`. nanokube rejects
  explicit non-matching values and overrides empty defaults.

`JoinConfiguration` documents are written by `nanokube add-node` rather
than supplied by the operator; boot refuses a config whose shape does not
match the node's recorded role.

## Commands

| Command | Description |
|---|---|
| `nanokube init` | Initialise a fresh control-plane node (run once per install) |
| `nanokube boot` | Internal: boot lifecycle, invoked by `nanokube.service` |
| `nanokube reset --yes` | Tear down all nanokube-managed state |
| `nanokube healthcheck` | Report cluster component health |
| `nanokube config print-defaults` | Emit a starter config template |
| `nanokube kubeconfig` | Manage kubeconfigs |
| `nanokube token create` | Mint a bootstrap token and print joining credentials |
| `nanokube add-node` | Join this node to an existing cluster as a worker |
| `nanokube version` | Print version |

### Adding a worker node

On a control-plane node, mint a bootstrap token:

```
nanokube token create
```

The output includes the token and CA pin. On the joining node (fresh bootc
image, `nanokube.service` not yet enabled), run:

```
nanokube add-node --server https://<cp>:6443 --token <t> --ca-cert-hash sha256:<h>
systemctl enable nanokube.service
```

`add-node` uses kubeadm token discovery to fetch the cluster config from
the control plane, writes it as a `JoinConfiguration`-shaped
`/etc/nanokube/config.yaml`, marks the node's role as `worker`, then
issues a blocking `systemctl restart nanokube.service`. The restart
returns only after `nanokube.service` signals `READY=1`, which happens once
the kubelet passes its healthz check, TLS-bootstrap completes, and the
node reports Ready.

The token TTL defaults to 24 hours and can be overridden with
`--ttl` (e.g. `nanokube token create --ttl 1h`).

The cluster's CNI must already be deployed; a worker node cannot reach
Ready without a functioning overlay network. The cluster-info ConfigMap and
TLS-bootstrap RBAC are set up automatically on any cluster initialised or
booted by this nanokube version.

#### Failed join recovery

A failed join leaves `nanokube.service` in failed state (`Restart=no`).
Rebooting the node in that state causes greenboot's `required.d` check to
fail; once the boot counter is exhausted, bootc rolls back and the restore
marker fires. On a fresh worker the restore is a no-op, but the restart
loop is wasteful. The correct recovery path is:

```
nanokube reset --yes
```

Then mint a fresh token on the control plane and re-run `add-node`.

## Development

### Running the internal/ddi tests locally

`internal/ddi` shells out to `systemd-repart --make-ddi=confext` and
erofs-utils, so its tests run inside a Fedora container (mirroring the
CI `ddi` job) rather than on the host toolchain. From the repo root:

```
WORK=$(mktemp -d ~/.cache/nanokube-ddi.XXXXXX)
podman run --rm --cap-add SYS_ADMIN --security-opt label=disable \
  -v "$WORK":/work -e TMPDIR=/work \
  -e GOCACHE=/work/gocache -e GOMODCACHE=/work/gomod -e GOFLAGS=-buildvcs=false \
  -v "$PWD":/src:ro -w /src \
  quay.io/fedora/fedora:42 \
  bash -c 'dnf install -y -q golang systemd-udev erofs-utils selinux-policy-targeted container-selinux libselinux-utils && go test -count=1 -v ./internal/ddi/...'
```

Rootless podman is sufficient; no sudo.

The `TMPDIR` redirection is what makes `TestBuildAppliesSELinuxLabels`
pass on an SELinux-enabled host: the test reads back baked-in
`security.selinux` xattrs via `fsck.erofs --extract --xattrs`, and that
write is rejected with ENOTSUP on the container's own overlay rootfs at
any privilege level, while a bind-mounted host directory accepts it
(see the `erofsXattr` comment in `internal/ddi/ddi_test.go`). Use a
directory on a persistent filesystem such as `~/.cache`, not `/tmp`.
`GOFLAGS=-buildvcs=false` is needed because the read-only source mount
hides `.git` from `go build`.
