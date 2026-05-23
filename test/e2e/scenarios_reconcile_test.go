//go:build e2e

package e2e

import (
	"os"
	"strings"
	"time"

	"github.com/MatchaScript/nanokube/test/e2etest"
)

// Test12Reconcile_MissingManifestReconciles simulates a disk tamper —
// delete kube-scheduler's static-pod manifest — and confirms that
// `nanokube boot`'s reconcile path re-creates the manifest and the
// scheduler returns to Ready after a service restart. This is the
// core "self-healing on reboot" assertion.
// Mirrors bash :test_abnormal_missing_manifest_reconciles.
func (s *NanokubeE2ESuite) Test12Reconcile_MissingManifestReconciles() {
	manifest := "/etc/kubernetes/manifests/kube-scheduler.yaml"
	s.T().Logf("deleting %s to simulate disk tamper", manifest)
	s.Require().NoError(os.Remove(manifest))

	s.T().Log("restarting nanokube.service")
	s.H.SystemctlRestart("nanokube.service")
	s.Require().True(s.H.SystemctlIsActive("nanokube.service"),
		"nanokube.service inactive after restart")

	e2etest.AssertFilePresent(s.T(), manifest, "Ensure must re-create kube-scheduler.yaml")

	s.H.WaitForStaticPodReady("kube-scheduler-"+s.H.NodeName(),
		"kube-system", 3*time.Minute)
}

// Test13Reconcile_IdempotentReboot asserts that restarting nanokube
// on a healthy, same-version cluster is a true no-op: the node stays
// Ready, last-event records no failure, and no "upgraded" marker is
// recorded (which would imply a version drift detection bug).
// Mirrors bash :test_normal_idempotent_reboot.
func (s *NanokubeE2ESuite) Test13Reconcile_IdempotentReboot() {
	s.H.SystemctlRestart("nanokube.service")
	s.Require().True(s.H.SystemctlIsActive("nanokube.service"),
		"2nd restart failed")
	s.H.WaitForNodeReady(3 * time.Minute)

	event, err := os.ReadFile("/var/lib/nanokube/state/last-event")
	s.Require().NoError(err, "read last-event")

	body := string(event)
	s.Require().NotContainsf(strings.ToLower(body), "failed",
		"last-event reports failure on idempotent restart: %s", body)
	s.Require().NotContainsf(body, "upgraded",
		"last-event reports upgrade on same-version restart: %s", body)
}
