//go:build e2e

package e2e

import (
	"github.com/MatchaScript/nanokube/test/e2etest"
)

// initArtifacts is the full set of paths `nanokube init` must produce
// AND leave on disk by the time it returns. super-admin.conf is
// deliberately NOT in this list: init writes it just-in-time to seed
// the kubeadm:cluster-admins ClusterRoleBinding and removes it before
// returning, so it never lingers on a long-lived node (see
// initialize.Run + removeSuperAdminKubeconfig).
//
// Drift here is the most common e2e regression signal — keep this
// list in sync with kubeadm.Ensure + EnsureAddons output minus init's
// post-RBAC cleanup.
var initArtifacts = []string{
	"/etc/kubernetes/pki/ca.crt",
	"/etc/kubernetes/pki/apiserver.crt",
	"/etc/kubernetes/pki/etcd/ca.crt",
	"/etc/kubernetes/pki/etcd/server.crt",
	"/etc/kubernetes/pki/sa.key",
	"/etc/kubernetes/admin.conf",
	"/etc/kubernetes/controller-manager.conf",
	"/etc/kubernetes/scheduler.conf",
	"/etc/kubernetes/kubelet.conf",
	"/etc/kubernetes/manifests/etcd.yaml",
	"/etc/kubernetes/manifests/kube-apiserver.yaml",
	"/etc/kubernetes/manifests/kube-controller-manager.yaml",
	"/etc/kubernetes/manifests/kube-scheduler.yaml",
	"/var/lib/kubelet/config.yaml",
	"/var/lib/kubelet/kubeadm-flags.env",
}

// Test04Init_WritesAllArtifacts asserts `nanokube init` produces every
// expected PKI / kubeconfig / manifest / kubelet artefact and writes
// the last-event state marker that state.Exists() trips on.
// Mirrors bash :test_normal_bootstrap_writes_all_artifacts.
//
// Note: the bash suite uses the legacy `bootstrap` verb; the CLI verb
// is now `init`. The Go port uses the current verb.
func (s *NanokubeE2ESuite) Test04Init_WritesAllArtifacts() {
	s.H.Nanokube("init")
	for _, p := range initArtifacts {
		e2etest.AssertFilePresent(s.T(), p, "init artifact")
	}
	e2etest.AssertFilePresent(s.T(),
		"/var/lib/nanokube/state/last-event", "init event marker")
}

// Test05Init_RefusesWhenStateExists asserts that re-running init
// refuses when state already exists, protecting against accidental
// cert blow-away. Depends on Test04 having created state. Mirrors
// bash :test_abnormal_bootstrap_refuses_when_state_exists.
//
// (The bash suite also had a `bootstrap --force` overwrite test;
// the current init CLI exposes no --force flag. The supported
// recovery path is `reset --yes` then `init`, exercised by Test16.)
func (s *NanokubeE2ESuite) Test05Init_RefusesWhenStateExists() {
	s.H.NanokubeExpectFail("init")
}
