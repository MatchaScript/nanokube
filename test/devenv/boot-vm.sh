#!/usr/bin/env bash
# Boot the nanokube devenv bootc VM with plain qemu + KVM (no libvirt daemon
# needed). SSH is reachable on the host at 127.0.0.1:${SSH_PORT} (default
# 2222). The guest can reach the host's local OCI registry (see
# registry.md) at 10.0.2.2:${REGISTRY_PORT} (default 5000) via the qemu
# user-mode network gateway alias.
#
# This only ever touches disposable state under /var/tmp/nanokube-devenv/ and
# a qemu process on the *host* -- it does not touch this host's own bootc/OS
# state.
set -euo pipefail

STATE_DIR="/var/tmp/nanokube-devenv"
DISK="${STATE_DIR}/output/qcow2/disk.qcow2"
SSH_PORT="${SSH_PORT:-2222}"
MEMORY_MB="${MEMORY_MB:-4096}"
VCPUS="${VCPUS:-2}"
PIDFILE="${STATE_DIR}/qemu.pid"
SERIAL_LOG="${STATE_DIR}/logs/serial-console.log"
MONITOR_SOCK="${STATE_DIR}/qemu-monitor.sock"
OVMF_CODE="/usr/share/edk2/ovmf/OVMF_CODE.fd"
OVMF_VARS_TEMPLATE="/usr/share/edk2/ovmf/OVMF_VARS.fd"
OVMF_VARS="${STATE_DIR}/ovmf-vars.fd"

if [ ! -f "${DISK}" ]; then
  echo "error: disk image not found at ${DISK}" >&2
  echo "build it first with build-image.sh" >&2
  exit 1
fi

mkdir -p "${STATE_DIR}/logs"

# Fedora bootc images are UEFI-only; give each VM its own writable NVRAM
# copy so it doesn't share/corrupt the read-only template.
if [ ! -f "${OVMF_VARS}" ]; then
  cp "${OVMF_VARS_TEMPLATE}" "${OVMF_VARS}"
fi

exec qemu-system-x86_64 \
  -name nanokube-devenv \
  -machine q35,accel=kvm \
  -cpu host \
  -smp "${VCPUS}" \
  -m "${MEMORY_MB}" \
  -drive if=pflash,format=raw,readonly=on,file="${OVMF_CODE}" \
  -drive if=pflash,format=raw,file="${OVMF_VARS}" \
  -drive file="${DISK}",if=virtio,format=qcow2 \
  -netdev user,id=net0,hostfwd=tcp::"${SSH_PORT}"-:22 \
  -device virtio-net-pci,netdev=net0 \
  -display none \
  -serial file:"${SERIAL_LOG}" \
  -monitor unix:"${MONITOR_SOCK}",server,nowait \
  -pidfile "${PIDFILE}" \
  -daemonize
