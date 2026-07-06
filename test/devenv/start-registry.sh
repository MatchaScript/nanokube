#!/usr/bin/env bash
# Start (or reuse) the local OCI registry that the devenv VM pulls updated
# bootc images from. Rootless podman container, bound to the host loopback
# only. Storage is disposable state under /var/tmp/nanokube-devenv/, not the
# git tree.
set -euo pipefail

STATE_DIR="/var/tmp/nanokube-devenv"
DATA_DIR="${STATE_DIR}/registry-data"
NAME="devenv-registry"
PORT="${REGISTRY_PORT:-5000}"

mkdir -p "${DATA_DIR}"

if podman container exists "${NAME}"; then
  if [ "$(podman inspect -f '{{.State.Running}}' "${NAME}")" != "true" ]; then
    podman start "${NAME}"
  else
    echo "${NAME} already running"
  fi
else
  podman run -d --name "${NAME}" \
    -p 127.0.0.1:"${PORT}":5000 \
    -v "${DATA_DIR}":/var/lib/registry:Z \
    docker.io/library/registry:2
fi

echo "registry listening at 127.0.0.1:${PORT} (host) / 10.0.2.2:${PORT} (from the qemu guest, via boot-vm.sh's user-mode network)"
