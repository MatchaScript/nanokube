package lifecycle

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/certs"
	"github.com/MatchaScript/nanokube/internal/state"
	"github.com/MatchaScript/nanokube/internal/testutil"
)

func TestShortID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"short", "short"},
		{"exactly12chr", "exactly12chr"},
		{"longerthantwelvechars", "longerthantw"},
	}
	for _, tc := range cases {
		if got := shortID(tc.in); got != tc.want {
			t.Errorf("shortID(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestShortPair_JoinsWithUnderscore(t *testing.T) {
	got := shortPair("deployment-abcdef-1234", "boot-id-abcdef-5678")
	// First 12 chars of each, joined.
	if got != "deployment-a_boot-id-abcd" {
		t.Errorf("shortPair = %q", got)
	}
}

// bootFailed writes a human-readable last-event and returns the original
// error verbatim. Both branches (upgrade vs. steady-state) must be
// distinguishable because operators read last-event from MOTD and the
// phrasing drives their debugging.
func TestBootFailed_WritesUpgradeEventAndReturnsCause(t *testing.T) {
	testutil.UseTempPaths(t)

	cause := errors.New("kubelet refused to start")
	err := bootFailed(true, "v1.35.0", "v1.36.0", cause)
	if !errors.Is(err, cause) {
		t.Errorf("bootFailed returned %v; must wrap cause", err)
	}
	event, err := state.ReadLastEvent()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"boot failed upgrading", "v1.35.0", "v1.36.0", "kubelet refused"} {
		if !strings.Contains(event, want) {
			t.Errorf("event %q missing %q", event, want)
		}
	}
}

func TestBootFailed_WritesSteadyStateEvent(t *testing.T) {
	testutil.UseTempPaths(t)

	cause := errors.New("ensure: PKI gone")
	_ = bootFailed(false, "", "v1.35.0", cause)

	event, err := state.ReadLastEvent()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event, "boot failed at v1.35.0") {
		t.Errorf("event %q missing 'boot failed at v1.35.0'", event)
	}
	if strings.Contains(event, "upgrading") {
		t.Errorf("event %q should not mention upgrade when not upgrading", event)
	}
}

// TestRenewLeavesIfStaleRotatesBelowThreshold pins down the scheduling
// order: when leaves issued at init are about to expire, the per-boot
// renewal helper must re-sign them. We don't run lifecycle.Boot end-
// to-end here (that would need systemd, kubelet, …); instead we drive
// the cert subsystem directly at the same lifecycle point and assert
// the helper does the right thing in isolation.
func TestRotateCertsIfStaleRotatesBelowThreshold(t *testing.T) {
	cfg := newTestConfigWithShortLeaves()
	layout := newTestCertsLayout(t)

	signer := certs.NewSigner(cfg, layout, "node-1")
	if err := signer.EnsureAll(); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}
	apiBefore := readSerial(t, filepath.Join(layout.PKIDir, "apiserver.crt"))

	if err := rotateCertsIfStale(cfg, layout, "node-1", io.Discard); err != nil {
		t.Fatalf("rotateCertsIfStale: %v", err)
	}

	apiAfter := readSerial(t, filepath.Join(layout.PKIDir, "apiserver.crt"))
	if apiBefore == apiAfter {
		t.Error("apiserver.crt unchanged after rotateCertsIfStale on a 1d-validity install; renewal did not trigger")
	}
}

// Default validity (1 year) is well above the 4-month threshold, so
// the helper must be a no-op — no I/O on the leaf cert files.
func TestRotateCertsIfStaleNoopOnFreshCerts(t *testing.T) {
	cfg := newTestConfig()
	layout := newTestCertsLayout(t)

	signer := certs.NewSigner(cfg, layout, "node-1")
	if err := signer.EnsureAll(); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}
	apiBefore := readSerial(t, filepath.Join(layout.PKIDir, "apiserver.crt"))

	if err := rotateCertsIfStale(cfg, layout, "node-1", io.Discard); err != nil {
		t.Fatalf("rotateCertsIfStale: %v", err)
	}

	apiAfter := readSerial(t, filepath.Join(layout.PKIDir, "apiserver.crt"))
	if apiBefore != apiAfter {
		t.Errorf("apiserver.crt rotated despite ~365d remaining; renewal threshold logic is too eager")
	}
}

// helpers — local to lifecycle test package.

func newTestConfig() *v1alpha1.NanoKubeConfig {
	c := &v1alpha1.NanoKubeConfig{
		Metadata: v1alpha1.ObjectMeta{Name: "test"},
		Spec: v1alpha1.NanoKubeConfigSpec{
			ControlPlane: v1alpha1.ControlPlaneSpec{AdvertiseAddress: "192.168.10.10"},
			Certificates: v1alpha1.CertificatesSpec{
				SelfSigned: true,
				ExtraSANs:  []string{"nanokube.local", "10.0.0.5"},
			},
		},
	}
	v1alpha1.SetDefaults(c)
	return c
}

func newTestConfigWithShortLeaves() *v1alpha1.NanoKubeConfig {
	c := newTestConfig()
	c.Spec.Certificates.LeafValidityDays = 1
	return c
}

func newTestCertsLayout(t *testing.T) certs.Layout {
	t.Helper()
	root := t.TempDir()
	return certs.Layout{
		PKIDir:        filepath.Join(root, "pki"),
		KubeconfigDir: filepath.Join(root, "kubernetes"),
	}
}

func readSerial(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert.SerialNumber.String()
}
