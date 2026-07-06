# nanokube Step 1 devenv VM

A real Fedora bootc VM, built and booted on the devenv host with plain QEMU/KVM
(no libvirt daemon), for Step 1 real-machine verification (confext placement,
`bootc` staging -- no reboot needed). Everything here is disposable: the VM,
its disk, and the local registry live under `/var/tmp/nanokube-devenv/`, never
under this repo and never touching the devenv host's own bootc/OS state.

## Why this path (bink vs. qemu-direct vs. KubeVirt)

1. **bink** (`references/bootc-dev/bink`) was spiked first. It has no bare-VM
   primitive: `cluster start` unconditionally SSHes in and runs
   `kubeadm init --config /etc/kubernetes/kubeadm-config.yaml` right after
   cloud-init completes (`internal/cluster/init.go`), and `node add`
   unconditionally runs `cluster.Join` (kubeadm join) against an
   auto-discovered control-plane node (`internal/cli/node/add.go`). `node
   join` itself is stubbed (`return fmt.Errorf("not implemented yet")`). There
   is no flag or lower-level command that creates a VM without immediately
   joining/initializing a competing kubeadm cluster, and `internal/cluster`
   is not importable from outside bink's own module. Forking bink was out of
   scope for this time-boxed task; a `--skip-init`/`--skip-join` flag upstream
   would be a reasonable future ask but isn't attempted here.
2. **KubeVirt+CDI** turned out to be reachable from this host (`~/.kube/config`
   points at the real "lab" homelab cluster, which has `kubevirt.io` and
   `cdi.kubevirt.io` CRDs installed). This was deliberately **not** used: it's
   the user's real production homelab (7 physical nodes), and the deliverable
   itself calls for "a simple local OCI registry ... that the built VM can
   pull from" for later bootc-upgrade testing -- which implies a registry and
   VM on the *same* host. Pushing a devenv-only test image into a shared
   production cluster's registry path, and fighting the resulting
   cross-network reachability problem (the registry would need to be
   reachable from cluster nodes on a different physical LAN) is unnecessary
   risk and complexity for what qemu-direct solves trivially and locally.
3. **qemu-direct** (this directory): plain `qemu-system-x86_64 -enable-kvm`,
   no libvirt daemon required. Chosen as the fastest, lowest-risk, fully
   self-contained option once (1) ruled out bink and (2) ruled out the
   production KubeVirt cluster as inappropriate for this purpose.

## Image contents

`image/Containerfile` replicates the package lineage of the homelab
production images (read-only references, never modified):

- `homelab/containers/bootc/base/fedora.Containerfile` (base:
  `quay.io/fedora/fedora-bootc:latest`)
- `homelab/containers/bootc/kubernetes/base.Containerfile` (kubelet/kubeadm
  v1.35, cri-o, greenboot, same package list and cri-o/kubernetes repo
  definitions)

Plus exactly one intentional devenv-only patch (confirmed with the user):
`image/overlay/usr/lib/systemd/system/kubelet.service.d/10-kubeadm.conf` sets
`--config=/etc/kubernetes/kubelet-config.yaml` instead of the kubeadm default
(`/var/lib/kubelet/config.yaml`).

Everything else under `image/overlay/` is devenv-only provisioning glue with
no equivalent in the homelab repo (that repo's real-hardware bootstrap is a
separate kickstart/ignition layer, out of scope here):

- `etc/systemd/network/20-wired.network` -- DHCP on the qemu virtio NIC.
- `etc/containers/registries.conf.d/10-devenv-registry.conf` -- trusts
  `10.0.2.2:5000` (the qemu user-mode network's host alias) as an insecure
  (plain HTTP) registry, so the guest can pull from the devenv registry
  below.
- `usr/lib/tmpfiles.d/nanokube-devenv.conf` + a `devenv` user created via
  `useradd -M` (no `-m`): **load-bearing gotcha** -- `/home` is a symlink to
  `/var/home` on this ostree/bootc base, and content written under `/var` at
  *container build time* is not part of the deployed OS (only `/usr` and the
  default `/etc` are committed directly into the ostree deployment; `/var` is
  populated from scratch at first boot via `systemd-tmpfiles`, and `bootc
  container lint` flags exactly this class of bug as a `var-tmpfiles`
  warning). The devenv user's home dir and `authorized_keys` are recreated at
  boot by the tmpfiles.d `d`/`C` directives instead, with the key source
  living under `/usr/share/nanokube-devenv/` (which, like any other `/usr`
  content, is part of the committed image). Without this fix the SSH key
  would silently vanish on first boot and the VM would be unreachable.

## Usage

```
./build-image.sh    # podman build -> push to local registry -> bootc-image-builder -> qcow2
./boot-vm.sh         # qemu-system-x86_64 -enable-kvm, daemonized
```

SSH (key at `/var/tmp/nanokube-devenv/ssh/id_ed25519`, not in git):

```
ssh -i /var/tmp/nanokube-devenv/ssh/id_ed25519 -p 2222 devenv@127.0.0.1
```

`devenv` has passwordless sudo. Root login is disabled implicitly (no root
password/key is provisioned at all).

## Registry / bootc target-imgref

`start-registry.sh` runs a rootless `registry:2` container bound to
`127.0.0.1:5000` on the host, with data under
`/var/tmp/nanokube-devenv/registry-data/`.

`bootc-image-builder` (as of the version pulled here,
`quay.io/centos-bootc/bootc-image-builder:latest`) always records
`target-imgref` as whatever ref was used to build the disk -- there is no
`--target-imgref` override yet (tracked upstream:
[osbuild/bootc-image-builder#559](https://github.com/osbuild/bootc-image-builder/issues/559),
open as of 2026-07; note the repo was archived 2026-06-18). Since the image
was built from `localhost:5000/nanokube-devenv:latest` (host-side ref, needed
for the build step itself), the VM's freshly-built `bootc status` initially
tracks `localhost:5000/...` -- which resolves to the *guest's own* loopback,
not the host, and would never see updates.

Fix applied once per VM, from inside the guest, after first boot:

```
sudo bootc switch --transport registry 10.0.2.2:5000/nanokube-devenv:latest
```

(`10.0.2.2` is the qemu user-mode network's fixed host alias; it's already
trusted as insecure via the registries.conf.d file above.) After this,
`bootc status` / `bootc upgrade --check` correctly resolve against the
devenv host's local registry. Verified end-to-end: `bootc switch` staged a
new deployment against `10.0.2.2:5000/nanokube-devenv:latest` with the
correct digest.

To push an updated image later: rebuild with `build-image.sh` (or just the
`podman build` + `podman tag`/`push` portion of it) using the same
`localhost:5000/nanokube-devenv:latest` tag, then on the guest run `sudo
bootc upgrade` (or re-run the `bootc switch` above if the tag/ref needs to
change).

## Verified on this VM (2026-07-05)

- SSH reachable, passwordless sudo works.
- `systemd --version` -> 259 (>= 256 required for confext).
- `systemd-confext merge --mutable=yes` / `unmerge` round-tripped cleanly
  (created a throwaway extension under `/var/lib/confexts/`, merged it,
  confirmed the test file appeared under `/etc`, unmerged, confirmed it was
  gone; the throwaway extension was deleted afterward -- nothing left behind
  on the VM).
- `bootc status` / `bootc switch` work as described above.
- kubelet drop-in patch confirmed in place
  (`--config=/etc/kubernetes/kubelet-config.yaml`).
- kubeadm/kubelet/kubectl v1.35.6, cri-o 1.35.5 (matches the pinned
  `v1.35`/`stable` repo lineage from the homelab Containerfiles).

Not exercised here (explicitly out of scope per the task): an actual reboot
into a switched/upgraded deployment. Step 1 verification only needs confext
placement and bootc *staging*, not a reboot cycle.

## Runtime state (not in git)

All under `/var/tmp/nanokube-devenv/`:

- `ssh/` -- generated SSH keypair for the `devenv` user.
- `output/qcow2/disk.qcow2` -- the built bootable disk (~1.4 GiB).
- `ovmf-vars.fd` -- this VM's private UEFI NVRAM (copied from the system
  `OVMF_VARS.fd` template on first boot).
- `registry-data/` -- the local registry's blob storage.
- `bib-store/`, `bib-rpmmd/` -- bootc-image-builder's osbuild scratch dirs.
- `logs/` -- build logs and the VM's serial console log.
- `qemu.pid`, `qemu-monitor.sock` -- the running qemu process's pidfile and
  QMP monitor socket (see "Stopping the VM" below).

## Stopping the VM / cleaning up

```
kill "$(cat /var/tmp/nanokube-devenv/qemu.pid)"          # stop the VM
podman stop devenv-registry                               # stop the registry
rm -rf /var/tmp/nanokube-devenv                            # wipe all disposable state
```
