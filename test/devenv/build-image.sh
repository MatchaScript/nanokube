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
STATE_DIR="/var/tmp/nanokube-devenv"
SSH_DIR="${STATE_DIR}/ssh"
IMAGE_TAG="localhost/nanokube-devenv:latest"
REGISTRY_TAG="localhost:5000/nanokube-devenv:latest"

mkdir -p "${SSH_DIR}" "${STATE_DIR}/output" "${STATE_DIR}/bib-store" "${STATE_DIR}/bib-rpmmd" "${STATE_DIR}/logs"

if [ ! -f "${SSH_DIR}/id_ed25519" ]; then
  ssh-keygen -t ed25519 -N "" -C "nanokube-devenv" -f "${SSH_DIR}/id_ed25519"
fi
PUBKEY="$(cat "${SSH_DIR}/id_ed25519.pub")"

echo "== [1/4] podman build (rootless) =="
podman build \
  --build-arg "DEVENV_SSH_PUBKEY=${PUBKEY}" \
  -t "${IMAGE_TAG}" \
  -f "${SCRIPT_DIR}/image/Containerfile" \
  "${SCRIPT_DIR}/image"

echo "== [2/4] ensure local registry + push =="
"${SCRIPT_DIR}/start-registry.sh"
podman tag "${IMAGE_TAG}" "${REGISTRY_TAG}"
podman push --tls-verify=false "${REGISTRY_TAG}"

echo "== [3/4] pull into root's container storage (bootc-image-builder requires rootful podman + local storage) =="
sudo podman pull --tls-verify=false "${REGISTRY_TAG}"

echo "== [4/4] bootc-image-builder: containers-storage image -> qcow2 disk =="
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
