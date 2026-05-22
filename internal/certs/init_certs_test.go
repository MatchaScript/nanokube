package certs

import (
	"os"
	"path/filepath"
	"testing"
)

// Init produces a self-signed PKI with no operator input. Every CA and
// leaf is materialised under PKIDir.
func TestInitSelfSignsAllCAs(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	if err := Init(cfg, layout); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, p := range []string{
		"ca.crt", "ca.key",
		"etcd/ca.crt", "etcd/ca.key",
		"front-proxy-ca.crt", "front-proxy-ca.key",
		"sa.key", "sa.pub",
	} {
		if _, err := os.Stat(filepath.Join(layout.PKIDir, p)); err != nil {
			t.Errorf("%s not produced: %v", p, err)
		}
	}
}
