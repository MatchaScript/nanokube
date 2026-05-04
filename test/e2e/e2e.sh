#!/usr/bin/env bash
# End-to-end test suite for nanokube.
#
# Covers both the golden path (config → bootstrap → boot → ready cluster →
# workload connectivity) and the disruption paths (invalid config, missing
# manifest reconciliation, reset wipe, refusal to re-bootstrap without
# --force). Passing this suite implies every supported boot flow works on
# a clean Ubuntu runner.
#
# Structured after microshift's suites/greenboot + kubeadm's kinder
# init/reset actions: each test function owns its setup and assertions,
# and a single EXIT trap dumps systemd + kubectl diagnostics when
# anything fails.
#
# Entry points:
#   sudo ./test/e2e/e2e.sh        # full setup + all tests
#   sudo ./test/e2e/e2e.sh tests  # skip setup, just run tests
# Environment overrides: see lib.sh (KUBELET_VERSION, CRIO_VERSION, etc.).

set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=./setup.sh
source "$SCRIPT_DIR/setup.sh"

REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)

trap dump_diagnostics EXIT

# ============================================================================
# Test cases — "normal" = golden path assertion, "abnormal" = disruption or
# deliberate bad input. Ordered so earlier tests set up state that later
# tests depend on (bootstrap → boot → addons → workload → reconcile), with
# reset at the very end.
# ============================================================================

# --- config surface ---------------------------------------------------------

test_normal_print_defaults_is_valid() {
    local tmp
    tmp=$(mktemp)
    "$NANOK8S_BIN" config print-defaults >"$tmp"
    assert_contains "$(cat "$tmp")" "apiVersion: bootstrap.nanokube.io/v1alpha1" "print-defaults"
    assert_contains "$(cat "$tmp")" "selfSigned: true" "print-defaults"
    # print-defaults is always advertised as a starter template; it must
    # pass validate without mutation (except for the placeholder
    # advertiseAddress).
    sed -i -E "s|^([[:space:]]+advertiseAddress:[[:space:]]).*$|\1192.168.1.10|" "$tmp"
    if ! grep -qE "advertiseAddress: 192.168.1.10" "$tmp"; then
        die "sed substitution failed on print-defaults output"
    fi
    "$NANOK8S_BIN" --config "$tmp" config validate
    rm -f "$tmp"
}

test_abnormal_invalid_config_rejected() {
    local tmp
    tmp=$(mktemp)
    cat >"$tmp" <<'EOF'
apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
metadata:
  name: bad
spec:
  controlPlane:
    advertiseAddress: "nope-not-an-ip"
  runtime:
    criSocket: "http://wrong-scheme"
  certificates:
    selfSigned: true
EOF
    assert_command_fails "validate must reject bad config" \
        "$NANOK8S_BIN" --config "$tmp" config validate
    # Error must mention each offending field so operators can fix it.
    local out
    if out=$("$NANOK8S_BIN" --config "$tmp" config validate 2>&1); then :; fi
    assert_contains "$out" "advertiseAddress" "validate error"
    assert_contains "$out" "criSocket"        "validate error"
    rm -f "$tmp"
}

test_abnormal_unknown_field_rejected() {
    local tmp
    tmp=$(mktemp)
    cat >"$tmp" <<'EOF'
apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
spec:
  controlPlane:
    advertiseAddress: 192.168.1.10
  typoField: "oops"
  certificates:
    selfSigned: true
EOF
    assert_command_fails "unknown field must be rejected (UnmarshalStrict)" \
        "$NANOK8S_BIN" --config "$tmp" config validate
    rm -f "$tmp"
}

# --- bootstrap --------------------------------------------------------------

test_normal_bootstrap_writes_all_artifacts() {
    "$NANOK8S_BIN" bootstrap
    for p in \
        /etc/kubernetes/pki/ca.crt \
        /etc/kubernetes/pki/apiserver.crt \
        /etc/kubernetes/pki/etcd/ca.crt \
        /etc/kubernetes/pki/etcd/server.crt \
        /etc/kubernetes/pki/sa.key \
        /etc/kubernetes/admin.conf \
        /etc/kubernetes/super-admin.conf \
        /etc/kubernetes/controller-manager.conf \
        /etc/kubernetes/scheduler.conf \
        /etc/kubernetes/kubelet.conf \
        /etc/kubernetes/manifests/etcd.yaml \
        /etc/kubernetes/manifests/kube-apiserver.yaml \
        /etc/kubernetes/manifests/kube-controller-manager.yaml \
        /etc/kubernetes/manifests/kube-scheduler.yaml \
        /var/lib/kubelet/config.yaml \
        /var/lib/kubelet/kubeadm-flags.env
    do
        assert_file_present "$p" "bootstrap artifact"
    done

    # state.Exists() must now report true — the bootstrap command writes
    # last-event specifically to establish this.
    assert_file_present /var/lib/nanokube/state/last-event "bootstrap event marker"
}

test_abnormal_bootstrap_refuses_when_state_exists() {
    # Follow-up of the prior test: running bootstrap again without --force
    # must refuse so operators don't accidentally blow away certs.
    assert_command_fails "re-bootstrap without --force must refuse" \
        "$NANOK8S_BIN" bootstrap
}

test_normal_bootstrap_force_overwrites() {
    # --force is the escape hatch. It must succeed and leave a working
    # set of artefacts in place.
    "$NANOK8S_BIN" bootstrap --force
    assert_file_present /etc/kubernetes/manifests/kube-apiserver.yaml "force bootstrap"
}

# --- boot → ready cluster ---------------------------------------------------

test_normal_service_boots_to_ready() {
    log_info "Starting nanokube.service"
    systemctl start nanokube.service
    systemctl is-active --quiet nanokube.service \
        || die "nanokube.service inactive after start"

    wait_for_node_ready 5m

    # Every kubeadm-style control-plane pod must be Ready. A Ready node
    # alone isn't enough — controller-manager / scheduler crash loops
    # would be invisible.
    local nodename
    nodename=$(hostname | tr '[:upper:]' '[:lower:]')
    for pod in \
        "etcd-$nodename" \
        "kube-apiserver-$nodename" \
        "kube-controller-manager-$nodename" \
        "kube-scheduler-$nodename"
    do
        log_info "Waiting for static pod $pod Ready"
        kubectl wait --for=condition=Ready pod/"$pod" \
            -n kube-system --timeout=3m \
            || die "$pod did not reach Ready"
    done
}

test_normal_admin_rbac_bound() {
    # admin.conf should be fully authorised thanks to the
    # kubeadm:cluster-admins ClusterRoleBinding created by EnsureAdminRBAC.
    kubectl auth can-i '*' '*' --all-namespaces >/dev/null \
        || die "admin.conf not authorised for cluster-wide verbs"
    kubectl get clusterrolebinding kubeadm:cluster-admins >/dev/null \
        || die "kubeadm:cluster-admins CRB missing"
}

test_normal_node_marked_controlplane() {
    local nodename
    nodename=$(hostname | tr '[:upper:]' '[:lower:]')

    # Label applied by markcontrolplane phase.
    local label
    label=$(kubectl get node "$nodename" \
        -o jsonpath='{.metadata.labels.node-role\.kubernetes\.io/control-plane}')
    # The label value is the empty string; the assertion is that the key
    # exists, which we detect via the jsonpath query succeeding without a
    # literal "<no value>" marker in the output.
    if kubectl get node "$nodename" -o json \
        | jq -e '.metadata.labels | has("node-role.kubernetes.io/control-plane")' >/dev/null
    then
        :
    else
        die "control-plane label missing (value=$label)"
    fi

    # The e2e config sets nodeRegistration.taints: [] (see setup.sh) so
    # the lone node is schedulable. Verify the empty-list semantics flow
    # all the way through to MarkControlPlane: the default control-plane
    # taint must NOT be present.
    if kubectl get node "$nodename" -o json \
        | jq -e '.spec.taints[]? | select(.key == "node-role.kubernetes.io/control-plane")' >/dev/null
    then
        die "control-plane taint present despite nodeRegistration.taints=[] in config"
    fi
}

test_normal_addons_deployed() {
    # CoreDNS and kube-proxy are the only addons nanokube manages; both
    # should be reconciled by EnsureAddons.
    kubectl -n kube-system get deployment coredns >/dev/null \
        || die "CoreDNS deployment missing"
    kubectl -n kube-system get daemonset kube-proxy >/dev/null \
        || die "kube-proxy DaemonSet missing"
}

# --- CNI + workload end-to-end ----------------------------------------------

test_normal_cni_and_workload_connectivity() {
    log_info "Installing flannel CNI"
    kubectl apply -f "$FLANNEL_URL"
    wait_for_pods_ready kube-flannel 5m

    # CoreDNS can only schedule once CNI is ready — wait for it here,
    # not earlier. Kube-proxy runs in host network so it was already up.
    wait_for_pods_ready kube-system 5m

    log_info "Deploying nginx test workload"
    kubectl create deployment e2e-nginx --image=nginx:alpine
    kubectl expose deployment e2e-nginx --port=80 --target-port=80
    kubectl wait --for=condition=Available deployment/e2e-nginx --timeout=3m

    local svc_ip
    svc_ip=$(kubectl get svc e2e-nginx -o jsonpath='{.spec.clusterIP}')
    log_info "Curling ClusterIP http://$svc_ip"
    # Service IP routing takes a few seconds to settle after deployment;
    # retry instead of hoping for an instantaneous success.
    retry 10 3 bash -c "curl --max-time 5 -fsS http://$svc_ip | grep -q 'Welcome to nginx'" \
        || die "workload not reachable via ClusterIP"
}

# --- reconciliation (disrupt → reboot service → recover) --------------------

test_abnormal_missing_manifest_reconciles() {
    log_info "Deleting kube-scheduler manifest to simulate disk tamper"
    rm -f /etc/kubernetes/manifests/kube-scheduler.yaml

    log_info "Restarting nanokube.service"
    systemctl restart nanokube.service
    systemctl is-active --quiet nanokube.service || die "nanokube.service inactive after restart"

    assert_file_present /etc/kubernetes/manifests/kube-scheduler.yaml \
        "Ensure must have re-created kube-scheduler.yaml"

    local nodename
    nodename=$(hostname | tr '[:upper:]' '[:lower:]')
    kubectl wait --for=condition=Ready pod/"kube-scheduler-$nodename" \
        -n kube-system --timeout=3m \
        || die "kube-scheduler did not return to Ready after reconcile"
}

test_normal_idempotent_reboot() {
    # A second restart of a healthy cluster must be a no-op, leave
    # last-event in a healthy state, and not record an upgrade.
    systemctl restart nanokube.service
    systemctl is-active --quiet nanokube.service || die "2nd restart failed"
    wait_for_node_ready 3m

    local event
    event=$(cat /var/lib/nanokube/state/last-event)
    if [[ "$event" == *"failed"* ]]; then
        die "last-event reports failure on idempotent restart: $event"
    fi
    if [[ "$event" == *"upgraded"* ]]; then
        die "last-event reports upgrade on same-version restart: $event"
    fi
}

# --- reset ------------------------------------------------------------------

test_normal_reset_wipes_everything() {
    "$NANOK8S_BIN" reset --yes

    assert_file_absent /etc/kubernetes/manifests "reset"
    assert_file_absent /var/lib/etcd "reset"
    assert_file_absent /var/lib/nanokube "reset"

    # Reset must not leave kubelet.service running; static pods would
    # restart with half-wiped state otherwise.
    if systemctl is-active --quiet kubelet.service; then
        die "kubelet.service still active after reset"
    fi
}

test_abnormal_reset_requires_yes() {
    assert_command_fails "reset without --yes must refuse" \
        "$NANOK8S_BIN" reset
}

test_normal_bootstrap_after_reset_is_clean() {
    # Reset must leave the node in a state where bootstrap succeeds
    # without --force — otherwise `reset` didn't actually clear
    # state.Exists() markers.
    "$NANOK8S_BIN" bootstrap
    assert_file_present /etc/kubernetes/manifests/kube-apiserver.yaml "post-reset bootstrap"
    # Clean up so the trap diagnostics are meaningful.
    "$NANOK8S_BIN" reset --yes
}

# ============================================================================
# Runner
# ============================================================================

run_all_tests() {
    run_test "print-defaults output is itself valid"          test_normal_print_defaults_is_valid
    run_test "[abnormal] invalid config rejected"             test_abnormal_invalid_config_rejected
    run_test "[abnormal] unknown field rejected (strict)"     test_abnormal_unknown_field_rejected

    run_test "bootstrap writes every expected artefact"       test_normal_bootstrap_writes_all_artifacts
    run_test "[abnormal] re-bootstrap refused without --force" test_abnormal_bootstrap_refuses_when_state_exists
    run_test "bootstrap --force overwrites existing state"    test_normal_bootstrap_force_overwrites

    run_test "nanokube.service boots to Ready cluster"         test_normal_service_boots_to_ready
    run_test "admin.conf is bound to cluster-admins CRB"      test_normal_admin_rbac_bound
    run_test "node is labelled control-plane, taints=[] honoured" test_normal_node_marked_controlplane
    run_test "CoreDNS + kube-proxy addons deployed"           test_normal_addons_deployed

    run_test "CNI + workload connectivity end-to-end"         test_normal_cni_and_workload_connectivity

    run_test "[abnormal] missing manifest reconciles"         test_abnormal_missing_manifest_reconciles
    run_test "idempotent restart is a no-op"                  test_normal_idempotent_reboot

    run_test "reset wipes every managed path"                 test_normal_reset_wipes_everything
    run_test "[abnormal] reset refuses without --yes"         test_abnormal_reset_requires_yes
    run_test "bootstrap after reset starts clean"             test_normal_bootstrap_after_reset_is_clean
}

main() {
    if [[ "$EUID" -ne 0 ]]; then
        die "E2E suite must be run as root (sudo)"
    fi
    local mode=${1:-full}
    case "$mode" in
        full)
            setup
            run_all_tests
            ;;
        tests)
            run_all_tests
            ;;
        setup)
            setup
            ;;
        *)
            die "unknown mode: $mode (expected: full|tests|setup)"
            ;;
    esac
    log_info "All E2E tests passed"
}

main "$@"
