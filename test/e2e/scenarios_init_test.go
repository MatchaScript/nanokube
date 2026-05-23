//go:build e2e

package e2e

import (
	"github.com/MatchaScript/nanokube/test/e2etest"
)

// initArtifacts is the full set of paths `nanokube init` must produce.
// Drift here is the most common e2e regression signal — keep this list
// in sync with the kubeadm.Ensure + EnsureAddons output.
var initArtifacts = []string{
	"/etc/kubernetes/pki/ca.crt",
	"/etc/kubernetes/pki/apiserver.crt",
	"/etc/kubernetes/pki/etcd/ca.crt",
	"/etc/kubernetes/pki/etcd/server.crt",
	"/etc/kubernetes/pki/sa.key",
	"/etc/kubernetes/admin.conf",
	"/etc/kubernetes/super-admin.conf",
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
// without --force refuses, protecting against accidental cert blow-away.
// Depends on Test04 having created state. Mirrors bash
// :test_abnormal_bootstrap_refuses_when_state_exists.
func (s *NanokubeE2ESuite) Test05Init_RefusesWhenStateExists() {
	s.H.NanokubeExpectFail("init")
}

// Test06Init_ForceOverwrites asserts that `init --force` is the
// escape hatch and succeeds even when state.Exists().
// Mirrors bash :test_normal_bootstrap_force_overwrites.
func (s *NanokubeE2ESuite) Test06Init_ForceOverwrites() {
	s.H.Nanokube("init", "--force")
	e2etest.AssertFilePresent(s.T(),
		"/etc/kubernetes/manifests/kube-apiserver.yaml", "force init")
}
