//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"time"
)

// Test07Boot_ServiceBootsToReady starts nanokube.service and waits
// for the node and every kubeadm-style control-plane static pod to
// reach Ready. A Ready node alone is insufficient — controller-manager
// or scheduler crash loops would otherwise be invisible.
// Mirrors bash :test_normal_service_boots_to_ready.
func (s *NanokubeE2ESuite) Test07Boot_ServiceBootsToReady() {
	s.T().Log("starting nanokube.service")
	s.H.SystemctlStart("nanokube.service")
	s.Require().True(s.H.SystemctlIsActive("nanokube.service"),
		"nanokube.service inactive after start")

	s.H.WaitForNodeReady(5 * time.Minute)

	for _, role := range []string{"etcd", "kube-apiserver", "kube-controller-manager", "kube-scheduler"} {
		pod := role + "-" + s.H.NodeName()
		s.T().Logf("waiting for static pod %s Ready", pod)
		s.H.WaitForStaticPodReady(pod, "kube-system", 3*time.Minute)
	}
}

// Test08Boot_AdminRBACBound asserts admin.conf is fully authorised
// thanks to the kubeadm:cluster-admins ClusterRoleBinding seeded by
// EnsureAdminClusterRoleBinding.
// Mirrors bash :test_normal_admin_rbac_bound.
func (s *NanokubeE2ESuite) Test08Boot_AdminRBACBound() {
	s.H.Kubectl("auth", "can-i", "*", "*", "--all-namespaces")
	s.H.Kubectl("get", "clusterrolebinding", "kubeadm:cluster-admins")
}

// Test09Boot_NodeMarkedControlPlane verifies the markcontrolplane
// phase ran (the control-plane label is present) and that the
// nodeRegistration.taints=[] in the e2e config (set by setup.sh)
// flowed through to MarkControlPlane — the default control-plane
// taint must NOT be present.
// Mirrors bash :test_normal_node_marked_controlplane.
func (s *NanokubeE2ESuite) Test09Boot_NodeMarkedControlPlane() {
	raw := s.H.Kubectl("get", "node", s.H.NodeName(), "-o", "json")

	var node struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Taints []struct {
				Key string `json:"key"`
			} `json:"taints"`
		} `json:"spec"`
	}
	s.Require().NoError(json.Unmarshal([]byte(raw), &node), "parse node json")

	const cpLabel = "node-role.kubernetes.io/control-plane"
	_, hasLabel := node.Metadata.Labels[cpLabel]
	s.Require().True(hasLabel, "control-plane label missing")

	for _, t := range node.Spec.Taints {
		s.Require().NotEqualf(cpLabel, t.Key,
			"control-plane taint present despite nodeRegistration.taints=[] in config")
	}
}

// Test10Boot_AddonsDeployed asserts CoreDNS deployment and kube-proxy
// DaemonSet are present — these are the only addons nanokube manages
// via EnsureAddons. (Readiness is checked in Test11 after CNI is up.)
// Mirrors bash :test_normal_addons_deployed.
func (s *NanokubeE2ESuite) Test10Boot_AddonsDeployed() {
	s.H.Kubectl("-n", "kube-system", "get", "deployment", "coredns")
	s.H.Kubectl("-n", "kube-system", "get", "daemonset", "kube-proxy")
	// Belt-and-braces: kubectl get prints headers even when the resource
	// is missing if -o name is used; confirm a non-empty resource name.
	out := s.H.Kubectl("-n", "kube-system", "get", "deployment", "coredns", "-o", "name")
	s.Require().Equal("deployment.apps/coredns", strings.TrimSpace(out))
}
