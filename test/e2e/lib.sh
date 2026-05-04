#!/usr/bin/env bash
# Shared helpers for the nanokube E2E suite. Sourced by e2e.sh.
# No side effects on source — defines functions only.

set -Eeuo pipefail

# --- versions ---------------------------------------------------------------
# Pin every externally-fetched dependency. Adjust these together when bumping
# the target Kubernetes minor; CRI-O is allowed to be one minor behind
# kubelet (https://github.com/cri-o/cri-o/blob/main/compatibility-matrix.md).
: "${KUBELET_VERSION:=v1.35.0}"
: "${KUBECTL_VERSION:=v1.35.0}"
: "${CRIO_VERSION:=v1.34}"
: "${CNI_PLUGINS_VERSION:=v1.4.1}"
: "${CRICTL_VERSION:=v1.34.0}"
: "${FLANNEL_URL:=https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml}"

# --- paths ------------------------------------------------------------------
# All paths align with what the real nanokube image ships.
export KUBECONFIG=/etc/kubernetes/admin.conf
NANOK8S_BIN="${NANOK8S_BIN:-/usr/bin/nanokube}"
NANOK8S_CONFIG=/etc/nanokube/config.yaml
NANOK8S_SERVICE_UNIT=/etc/systemd/system/nanokube.service
KUBELET_SERVICE_UNIT=/etc/systemd/system/kubelet.service
CRIO_SERVICE=crio.service

# --- logging ----------------------------------------------------------------
RED=$'\033[0;31m'
GREEN=$'\033[0;32m'
YELLOW=$'\033[0;33m'
BLUE=$'\033[0;34m'
NC=$'\033[0m'

_log()      { printf '%s[%s]%s %s\n' "$1" "$2" "$NC" "$3"; }
log_info()  { _log "$GREEN"  INFO  "$*"; }
log_warn()  { _log "$YELLOW" WARN  "$*"; }
log_step()  { _log "$BLUE"   STEP  "$*"; }
log_err()   { _log "$RED"    ERROR "$*" >&2; }
die()       { log_err "$*"; exit 1; }

# --- failure handler --------------------------------------------------------
# Dumps every piece of state a human would look at when triaging a failing
# boot. Called from the EXIT trap only when the suite exits non-zero.
dump_diagnostics() {
    local rc=$?
    [[ "$rc" -eq 0 ]] && return 0

    log_err "E2E failure (rc=$rc) — collecting diagnostics"

    for unit in "$CRIO_SERVICE" kubelet.service nanokube.service; do
        echo "======== systemctl status $unit ========"
        systemctl status --no-pager --full "$unit" 2>&1 || true
        echo "======== journalctl -u $unit -n 200 ========"
        journalctl --no-pager -u "$unit" -n 200 2>&1 || true
    done

    echo "======== nanokube last-event ========"
    cat /var/lib/nanokube/state/last-event 2>/dev/null || echo "(absent)"
    echo
    echo "======== nanokube last-boot.json ========"
    cat /var/lib/nanokube/state/last-boot.json 2>/dev/null || echo "(absent)"
    echo

    if [[ -f "$KUBECONFIG" ]]; then
        echo "======== kubectl get nodes ========"
        kubectl get nodes -o wide 2>&1 || true
        echo "======== kubectl get pods -A ========"
        kubectl get pods -A -o wide 2>&1 || true
        echo "======== kubectl get events -A --sort-by=.lastTimestamp ========"
        kubectl get events -A --sort-by=.lastTimestamp 2>&1 | tail -40 || true
    fi

    if command -v crictl >/dev/null; then
        echo "======== crictl ps -a ========"
        crictl ps -a 2>&1 || true
    fi

    if [[ -d /etc/kubernetes/manifests ]]; then
        echo "======== /etc/kubernetes/manifests ========"
        ls -la /etc/kubernetes/manifests 2>&1 || true
    fi

    return "$rc"
}

# --- polling helpers --------------------------------------------------------
# retry <attempts> <delay-sec> <cmd...> — returns 0 when cmd exits 0 within
# the budget; returns the last exit code otherwise.
retry() {
    local attempts=$1 delay=$2
    shift 2
    local i
    for (( i = 1; i <= attempts; i++ )); do
        if "$@"; then return 0; fi
        if (( i < attempts )); then sleep "$delay"; fi
    done
    return 1
}

wait_for_node_ready() {
    local timeout=${1:-5m}
    log_info "Waiting for node Ready (timeout=$timeout)..."
    kubectl wait --for=condition=Ready node --all --timeout="$timeout"
}

wait_for_pods_ready() {
    local namespace=$1 timeout=${2:-5m}
    log_info "Waiting for all pods Ready in $namespace (timeout=$timeout)..."
    # Retry once: on a freshly-booted cluster the first wait can race against
    # a pod creation that has not yet hit the apiserver.
    retry 3 5 kubectl wait --for=condition=Ready pods --all -n "$namespace" --timeout="$timeout"
}

# assert_file_absent <path> <what>
assert_file_absent() {
    if [[ -e "$1" ]]; then
        die "$2: $1 still exists"
    fi
}

# assert_file_present <path> <what>
assert_file_present() {
    if [[ ! -e "$1" ]]; then
        die "$2: $1 missing"
    fi
}

# assert_contains <haystack> <needle> <what>
assert_contains() {
    if ! grep -Fq -- "$2" <<<"$1"; then
        die "$3: output did not contain '$2'. Got: $1"
    fi
}

# assert_command_fails <what> <cmd...>  — inverts exit status
assert_command_fails() {
    local what=$1; shift
    if "$@" >/dev/null 2>&1; then
        die "$what: expected failure but command succeeded"
    fi
}

# run_test <label> <fn>
run_test() {
    local label=$1 fn=$2
    log_step "TEST: $label"
    "$fn"
    log_info "PASSED: $label"
}
