//go:build e2e

package e2e

import (
	"os"
	"regexp"
	"strings"
	"time"
)

// Test06JoinPrereqs_ClusterObjectsExist asserts that EnsureJoinPrereqs
// seeded all kubeadm join-path objects: the three config ConfigMaps and
// the three bootstrap/rotation ClusterRoleBindings. Bare kubectl calls
// fail the test on non-zero exit.
func (s *NanokubeE2ESuite) Test06JoinPrereqs_ClusterObjectsExist() {
	s.H.Kubectl("get", "configmap", "kubeadm-config", "-n", "kube-system")
	s.H.Kubectl("get", "configmap", "kubelet-config", "-n", "kube-system")
	s.H.Kubectl("get", "configmap", "cluster-info", "-n", "kube-public")
	s.H.Kubectl("get", "clusterrolebinding",
		"kubeadm:kubelet-bootstrap",
		"kubeadm:node-autoapprove-bootstrap",
		"kubeadm:node-autoapprove-certificate-rotation")
}

// Test06JoinPrereqs_LastBootRecordsRole asserts init wrote
// role:"control-plane" into last-boot.json (Task 3).
func (s *NanokubeE2ESuite) Test06JoinPrereqs_LastBootRecordsRole() {
	b, err := os.ReadFile("/var/lib/nanokube/state/last-boot.json")
	s.Require().NoError(err)
	s.Contains(string(b), `"role":"control-plane"`)
}

// Test06JoinPrereqs_KubeletConfUsesRotationStore waits up to 3 minutes
// for the KCM to auto-approve the kubelet's first rotation CSR (the CRB
// added by EnsureJoinPrereqs), then confirms init's finalize step
// (Task 4) repointed kubelet.conf at the rotation store.
func (s *NanokubeE2ESuite) Test06JoinPrereqs_KubeletConfUsesRotationStore() {
	// The rotation store appears once the KCM auto-approves the
	// kubelet's first rotation CSR (the CRB this PR adds).
	deadline := time.Now().Add(3 * time.Minute)
	pem := "/var/lib/kubelet/pki/kubelet-client-current.pem"
	for {
		if _, err := os.Stat(pem); err == nil {
			break
		}
		if time.Now().After(deadline) {
			s.T().Fatalf("rotation store %s never appeared", pem)
		}
		time.Sleep(5 * time.Second)
	}
	// init's 90s finalize window (Task 4) must have repointed kubelet.conf.
	b, err := os.ReadFile("/etc/kubernetes/kubelet.conf")
	s.Require().NoError(err)
	s.Contains(string(b), "kubelet-client-current.pem")
}

// Test06JoinPrereqs_TokenCreate mints a join token via `nanokube token
// create`, checks the output format, and verifies the backing
// bootstrap-token Secret exists in kube-system.
func (s *NanokubeE2ESuite) Test06JoinPrereqs_TokenCreate() {
	out, _ := s.H.Nanokube("token", "create")
	s.Contains(out, "token: ")
	s.Contains(out, "ca-cert-hash: sha256:")
	tokenLine := regexp.MustCompile(`token: (\S+)`).FindStringSubmatch(out)
	s.Require().Len(tokenLine, 2)
	id := strings.SplitN(tokenLine[1], ".", 2)[0]
	s.H.Kubectl("get", "secret", "-n", "kube-system", "bootstrap-token-"+id)
}
