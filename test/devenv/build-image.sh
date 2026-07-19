#!/usr/bin/env bash
# Build the nanokube devenv bootc container image and convert it into a
# bootable qcow2 disk, using bootc-image-builder. Only touches:
#   - rootless podman image/container storage (this user's containers-storage)
#   - root's containers-storage (bootc-image-builder needs rootful podman;
#     see README.md "Why sudo podman" for why this is unavoidable)
#   - disposable state under /var/tmp/nanokube-devenv/
# It never touches this host's own bootc/OS state.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
STATE_DIR="${STATE_DIR:-/var/tmp/nanokube-devenv}"
SSH_DIR="${STATE_DIR}/ssh"
IMAGE_TAG="${IMAGE_TAG:-localhost/nanokube-devenv:latest}"
REGISTRY_TAG="${REGISTRY_TAG:-localhost:5000/nanokube-devenv:latest}"
AGENT_BIN_DIR="${SCRIPT_DIR}/image/bin"
PROTOC_DIR="${STATE_DIR}/tools/protoc"
PROTOC_VERSION="35.1"

PIDFILE="${STATE_DIR}/qemu.pid"
MARKER="${STATE_DIR}/.build-in-progress"

check_vm_not_running() {
  if [ -f "${PIDFILE}" ] && kill -0 "$(cat "${PIDFILE}")" 2>/dev/null; then
    echo "error: a devenv VM is currently running (pid $(cat "${PIDFILE}")), holding" >&2
    echo "  ${STATE_DIR}/output/qcow2/disk.qcow2 open as its backing disk." >&2
    echo "  Rebuilding overwrites that same path in place: bootc-image-builder writing a" >&2
    echo "  fresh qcow2 there while the running VM concurrently reads/writes it corrupts" >&2
    echo "  the live guest filesystem (confirmed: ext4 journal abort, unrecoverable" >&2
    echo "  qemu-img check errors -- see README's dated incident note)." >&2
    echo "  Stop the VM first: kill \"\$(cat ${PIDFILE})\"" >&2
    exit 1
  fi
}

check_vm_not_running

# Mark a build as in progress for the whole lifetime of this script, so
# boot-vm.sh (possibly started from another terminal) can refuse to boot
# against a disk we're about to overwrite. Cleared on any exit -- success,
# failure, or signal -- so a normal build-then-boot workflow never sees it.
mkdir -p "${STATE_DIR}"
echo "$$" >"${MARKER}"
trap 'rm -f "${MARKER}"' EXIT

mkdir -p "${SSH_DIR}" "${STATE_DIR}/output" "${STATE_DIR}/bib-store" "${STATE_DIR}/bib-rpmmd" "${STATE_DIR}/logs"

if [ ! -f "${SSH_DIR}/id_ed25519" ]; then
  ssh-keygen -t ed25519 -N "" -C "nanokube-devenv" -f "${SSH_DIR}/id_ed25519"
fi
PUBKEY="$(cat "${SSH_DIR}/id_ed25519.pub")"

echo "== [1/5] build nanokube-agent (release) =="
# The image itself carries no Rust toolchain, so the agent is built here on
# the host (same glibc/arch as the guest -- see README) and COPY'd into the
# image by the Containerfile, rather than adding a Rust builder stage.
if command -v protoc >/dev/null 2>&1; then
  PROTOC_BIN="$(command -v protoc)"
else
  # Not installed via dnf deliberately: this host's own bootc/OS state is
  # off-limits (see README/task notes), so a missing `protoc` is fetched as a
  # pinned static build into disposable devenv state instead of a system package.
  if [ ! -x "${PROTOC_DIR}/bin/protoc" ]; then
    echo "protoc not found on PATH; fetching a pinned static build into ${PROTOC_DIR}"
    mkdir -p "${PROTOC_DIR}"
    curl -sSL -o "${STATE_DIR}/protoc.zip" \
      "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-x86_64.zip"
    unzip -o -q "${STATE_DIR}/protoc.zip" -d "${PROTOC_DIR}"
    rm -f "${STATE_DIR}/protoc.zip"
  fi
  PROTOC_BIN="${PROTOC_DIR}/bin/protoc"
fi
PROTOC="${PROTOC_BIN}" cargo build --release --manifest-path "${REPO_ROOT}/agent/Cargo.toml"
mkdir -p "${AGENT_BIN_DIR}"
cp "${REPO_ROOT}/agent/target/release/nanokube-agent" "${AGENT_BIN_DIR}/nanokube-agent"

echo "== [2/5] podman build (rootless) =="
podman build \
  --build-arg "DEVENV_SSH_PUBKEY=${PUBKEY}" \
  -t "${IMAGE_TAG}" \
  -f "${SCRIPT_DIR}/image/Containerfile" \
  "${SCRIPT_DIR}/image"

# erofs-utils (added to the Containerfile's package list) and
# selinux-policy-targeted (expected from the fedora-bootc base) are both
# required for on-node DDI builds and SELinux label verification
# (nanokube Step 2 Task 12 / Step 3 Task B). Verify rootlessly against the
# image just built rather than trusting the package list silently.
podman run --rm "${IMAGE_TAG}" rpm -q erofs-utils selinux-policy-targeted

echo "== [3/5] ensure local registry + push =="
"${SCRIPT_DIR}/start-registry.sh"
podman tag "${IMAGE_TAG}" "${REGISTRY_TAG}"
podman push --tls-verify=false "${REGISTRY_TAG}"

echo "== [4/5] pull into root's container storage (bootc-image-builder requires rootful podman + local storage) =="
sudo podman pull --tls-verify=false "${REGISTRY_TAG}"

echo "== [5/5] bootc-image-builder: containers-storage image -> qcow2 disk =="
# Re-check right before the step that actually overwrites disk.qcow2: the
# cargo/podman/push/pull sequence above takes minutes, long enough for a VM
# to have been started (e.g. boot-vm.sh in another terminal) since the check
# at script start. Catch that here instead of corrupting a live disk.
check_vm_not_running
sudo podman run --rm --privileged \
  --network=host \
  --security-opt label=disable \
  -v "${STATE_DIR}/output":/output \
  -v "${STATE_DIR}/bib-store":/store \
  -v "${STATE_DIR}/bib-rpmmd":/rpmmd \
  -v /var/lib/containers/storage:/var/lib/containers/storage \
  --log-level info \
  quay.io/centos-bootc/bootc-image-builder:latest \
  build --type qcow2 --rootfs ext4 "${REGISTRY_TAG}"

sudo chown "$(id -u):$(id -g)" "${STATE_DIR}/output/qcow2/disk.qcow2" "${STATE_DIR}/output/manifest-qcow2.json"

echo "disk image: ${STATE_DIR}/output/qcow2/disk.qcow2"
echo "next: ./boot-vm.sh"
