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

// Test02Config_InvalidConfigRejected asserts that validate rejects a
// config with a bad IP and a bad CRI socket scheme, and that the
// error message names each offending field. Mirrors bash
// :test_abnormal_invalid_config_rejected.
func (s *NanokubeE2ESuite) Test02Config_InvalidConfigRejected() {
	body := `apiVersion: bootstrap.nanokube.io/v1alpha1
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
`
	tmp := filepath.Join(s.T().TempDir(), "bad.yaml")
	s.Require().NoError(os.WriteFile(tmp, []byte(body), 0o644))

	stdout, stderr := s.H.NanokubeExpectFail("--config", tmp, "config", "validate")
	combined := stdout + stderr
	e2etest.AssertContains(s.T(), combined, "advertiseAddress", "validate error")
	e2etest.AssertContains(s.T(), combined, "criSocket", "validate error")
}

// Test03Config_UnknownFieldRejected asserts that an unknown top-level
// field is rejected (UnmarshalStrict semantics).
// Mirrors bash :test_abnormal_unknown_field_rejected.
func (s *NanokubeE2ESuite) Test03Config_UnknownFieldRejected() {
	body := `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
spec:
  controlPlane:
    advertiseAddress: 192.168.1.10
  typoField: "oops"
  certificates:
    selfSigned: true
`
	tmp := filepath.Join(s.T().TempDir(), "typo.yaml")
	s.Require().NoError(os.WriteFile(tmp, []byte(body), 0o644))

	s.H.NanokubeExpectFail("--config", tmp, "config", "validate")
}
