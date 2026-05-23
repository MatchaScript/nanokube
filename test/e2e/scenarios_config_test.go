//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/MatchaScript/nanokube/test/e2etest"
)

// Test01Config_PrintDefaultsIsValid asserts `nanokube config
// print-defaults` emits a valid starter template that, after the
// advertiseAddress placeholder is filled in, passes `config validate`.
// Mirrors test/e2e/e2e.sh:test_normal_print_defaults_is_valid.
func (s *NanokubeE2ESuite) Test01Config_PrintDefaultsIsValid() {
	out, _ := s.H.Nanokube("config", "print-defaults")
	e2etest.AssertContains(s.T(), out, "apiVersion: bootstrap.nanokube.io/v1alpha1", "print-defaults")
	e2etest.AssertContains(s.T(), out, "selfSigned: true", "print-defaults")

	re := regexp.MustCompile(`(?m)^(\s+advertiseAddress:\s).*$`)
	rewritten := re.ReplaceAllString(out, "${1}192.168.1.10")
	s.Require().Contains(rewritten, "advertiseAddress: 192.168.1.10",
		"sed substitution failed on print-defaults output")

	tmp := filepath.Join(s.T().TempDir(), "defaults.yaml")
	s.Require().NoError(os.WriteFile(tmp, []byte(rewritten), 0o644))
	s.H.Nanokube("--config", tmp, "config", "validate")
}

// Test02Config_InvalidConfigRejected asserts validate rejects a config
// with a bad advertiseAddress and names the offending field. The bash
// original also asserted the error mentioned criSocket; that no longer
// holds under the kubeadm multi-document schema (kubeadm's validator
// fails fast on the first bad field and a non-scheme CRI URI only
// trips a deprecation warning), so the assertion is narrowed to
// advertiseAddress — the consistently-rejected case.
// Mirrors bash :test_abnormal_invalid_config_rejected, adapted to the
// post-23b7b53 schema.
func (s *NanokubeE2ESuite) Test02Config_InvalidConfigRejected() {
	body := `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
metadata:
  name: bad
spec: {}
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: nope-not-an-ip
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
`
	tmp := filepath.Join(s.T().TempDir(), "bad.yaml")
	s.Require().NoError(os.WriteFile(tmp, []byte(body), 0o644))

	stdout, stderr := s.H.NanokubeExpectFail("--config", tmp, "config", "validate")
	// kubeadm reports the field via its CLI-flag name
	// (apiserver-advertise-address); that substring keeps both "advertise"
	// and "address" visible in the error so the assertion still verifies
	// "the offending field is named".
	e2etest.AssertContains(s.T(), stdout+stderr, "advertise-address", "validate error")
}

// Test03Config_UnknownFieldRejected asserts an unknown field under
// spec is rejected (NanoKubeConfigSpec is empty {} now; any field is
// strict-unknown). Mirrors bash :test_abnormal_unknown_field_rejected.
func (s *NanokubeE2ESuite) Test03Config_UnknownFieldRejected() {
	body := `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
spec:
  typoField: oops
`
	tmp := filepath.Join(s.T().TempDir(), "typo.yaml")
	s.Require().NoError(os.WriteFile(tmp, []byte(body), 0o644))

	stdout, stderr := s.H.NanokubeExpectFail("--config", tmp, "config", "validate")
	e2etest.AssertContains(s.T(), stdout+stderr, "typoField", "unknown-field error")
}
