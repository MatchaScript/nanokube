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

### nanokube-agent (baked in, 2026-07-06)

The real `nanokube-agent` release binary is baked into the image and started
by an enabled systemd unit -- this is Step 1 item 1's residual work, not
devenv provisioning glue:

- `build-image.sh` builds `agent/` in release mode on the host (`cargo build
  --release --manifest-path agent/Cargo.toml`) before the `podman build`
  step, and copies the resulting binary into `image/bin/nanokube-agent`
  (gitignored, matches the existing root `.gitignore`'s `bin/` pattern) so
  the Containerfile can `COPY` it in. No Rust builder stage was added to the
  Containerfile itself -- host and guest are both Fedora 44/glibc
  2.43/x86_64 (confirmed via `ldd --version`/`/etc/os-release` on both
  sides), so a plain host-built binary runs on the guest unmodified, and
  this avoids adding a second toolchain-carrying build stage for no benefit.
  `protoc` (needed by the agent's build script) is deliberately *not*
  installed via `dnf` on the host -- this host is itself a bootc system
  whose own OS state must not be touched by this devenv -- so
  `build-image.sh` fetches a pinned static `protoc` release into
  `/var/tmp/nanokube-devenv/tools/protoc/` (disposable state) instead, if
  one isn't already on `PATH`.
- The binary lands at `/usr/local/bin/nanokube-agent`; the unit is
  `image/overlay/usr/lib/systemd/system/nanokube-agent.service`, `systemctl
  enable`'d alongside the image's other units. It runs `nanokube-agent serve
  --listen 0.0.0.0:9090 --confexts-dir /var/lib/confexts --bookkeeping-path
  /var/lib/nanokube/state/agent-bookkeeping.json` (matching
  `agent/src/main.rs`'s own defaults -- spelled out explicitly in the unit
  for self-documentation). No tmpfiles.d entries were needed for those `/var`
  paths: `RealOps::place`/`atomic_write` already `create_dir_all` them on
  first use.
- `boot-vm.sh` forwards the agent's gRPC port permanently now:
  `hostfwd=tcp::${AGENT_PORT}-:9090` (default `AGENT_PORT=9090`) alongside
  the existing SSH forward, so `127.0.0.1:9090` on the host always reaches
  the guest agent for any VM booted with this script -- no more live
  monitor-socket `hostfwd_add` needed after a fresh boot.

**Rebuilding while a VM is running**: `build-image.sh`'s `bootc-image-builder`
step writes a brand-new `output/qcow2/disk.qcow2` at the *same path* a
running VM has open as its backing disk. Doing so while `boot-vm.sh`'s qemu
process is still up corrupts the live guest's filesystem (confirmed
2026-07-06 -- see below). `build-image.sh` now refuses to run if
`qemu.pid` names a live process; stop the VM first (see "Stopping the VM"),
rebuild, then `boot-vm.sh` again.

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

## Verified on this VM (2026-07-06): agent baked in, reboot, Kind reachability

Step 1 item 1's residual work: bake `nanokube-agent` into the image, prove
Kind (podman container) -> agent (qemu guest) gRPC reachability.

**Incident, contained**: the first `build-image.sh` run of the day executed
while the 2026-07-05 VM was still running. `bootc-image-builder` writes a
fresh `output/qcow2/disk.qcow2` at the same path that VM's qemu process had
open as its live backing disk; the concurrent writes corrupted it (`ext4`
journal aborted mid-boot, filesystem remounted read-only, `qemu-img check`
reported dozens of unrecoverable refcount errors). Per this task's own
"everything here is disposable" rule: the corrupted VM was killed and
discarded, the image was rebuilt clean (`qemu-img check` afterward: "No
errors were found"), and a fresh VM was booted from it. `build-image.sh` now
refuses to run while `qemu.pid` names a live process, so this can't
recur silently.

- Fresh VM booted from the rebuilt image; `bootc status` showed the
  expected first-boot quirk (booted image `localhost:5000/...`); reapplied
  `sudo bootc switch --transport registry 10.0.2.2:5000/nanokube-devenv:latest`,
  staged correctly, then `sudo systemctl reboot`'d for real (the
  2026-07-05 entry above didn't exercise this). Post-reboot `bootc status`
  confirms booted image `10.0.2.2:5000/nanokube-devenv:latest`, with the
  prior `localhost:5000/...` deployment retained as `rollback`.
- `systemctl status nanokube-agent` post-reboot: `active (running)`,
  listening on `0.0.0.0:9090`. Confirms the unit survives a real reboot, not
  just a first boot.
- Host reachability: `boot-vm.sh`'s new permanent hostfwd applied
  automatically on this boot (no live monitor-socket patch needed) --
  `ss -tlnp` on the host shows `qemu-system-x86` on `127.0.0.1:9090`, and a
  bare TCP connect from the host succeeds.
- A real `PushDesired` round trip (via a locally-built `grpcurl`, not
  installed system-wide) against `127.0.0.1:9090` reached the real agent
  process and drove its real pipeline: checksum verification passed,
  `place` wrote the confext file under `/var/lib/confexts/`, then
  `systemd-confext refresh --mutable=yes` correctly rejected the test
  payload ("Failed to read metadata for image ...: Package not installed")
  since it was arbitrary bytes, not a real confext DDI image -- stronger
  evidence than a bare handshake, since it proves the full server-side path
  executes for real. The stray `.raw`/`.generations` files this left under
  `/var/lib/confexts/` were removed afterward (bookkeeping was never
  written -- `refresh` fails before that step).
- Registry: `curl http://127.0.0.1:5000/v2/_catalog` (host) and the
  equivalent `http://10.0.2.2:5000/v2/_catalog` from inside the guest both
  list `nanokube-devenv:latest`. `sudo bootc upgrade --check` on the guest
  reports "No changes in: docker://10.0.2.2:5000/nanokube-devenv:latest",
  confirming the guest can actually resolve and pull manifests from its own
  view of the registry, not just reach it at the TCP level.
- Kind -> agent reachability: `KIND_EXPERIMENTAL_PROVIDER=podman kind create
  cluster` (no docker daemon socket on this host -- confirmed via `docker
  info`, hence the podman provider). From a plain busybox pod on that
  cluster, `nc -zv -w3 169.254.1.2 9090` reports `open`. `169.254.1.2` is
  rootless podman's `pasta` networking backend's host-loopback alias (this
  host's `podman info` confirms `rootlessNetworkCmd: pasta`) -- neither
  `hostNetwork: true` nor the podman bridge gateway IP reach the host from
  inside a Kind pod on this host, only this address does. The test cluster
  and pod were deleted afterward (`kind delete cluster`).

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
