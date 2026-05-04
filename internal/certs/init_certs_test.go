package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFakeCA generates a throwaway self-signed CA pair into <dir>/<base>.{crt,key}.
// Used to simulate operator-supplied CAs without standing up a real PKI.
func writeFakeCA(t *testing.T, dir, baseName, cn string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, baseName+".crt")
	keyPath := filepath.Join(dir, baseName+".key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	keyDER := x509.MarshalPKCS1PrivateKey(key)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInitNoSeedSelfSignsEverything(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)
	if err := Init(cfg, layout, "node-1"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.PKIDir, "ca.crt")); err != nil {
		t.Errorf("ca.crt not produced: %v", err)
	}
}

// Operator drops cluster CA only → cluster CA copied verbatim, etcd /
// front-proxy CAs are self-signed by EnsureAll.
func TestInitClusterCAOnlyIsCopied(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)
	writeFakeCA(t, layout.OperatorDir, "ca", "byoca-cluster")

	seedBytes, err := os.ReadFile(filepath.Join(layout.OperatorDir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}

	if err := Init(cfg, layout, "node-1"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pkiBytes, err := os.ReadFile(filepath.Join(layout.PKIDir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(seedBytes) != string(pkiBytes) {
		t.Error("cluster CA was not copied verbatim from OperatorDir")
	}

	// etcd CA must exist (self-signed) — its content differs from the
	// cluster CA.
	if _, err := os.Stat(filepath.Join(layout.PKIDir, "etcd", "ca.crt")); err != nil {
		t.Errorf("etcd CA not generated: %v", err)
	}
}

// All three CAs supplied → all three copied; nothing self-signed.
func TestInitAllThreeCAsCopied(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)
	writeFakeCA(t, layout.OperatorDir, "ca", "byoca-cluster")
	writeFakeCA(t, filepath.Join(layout.OperatorDir, "etcd"), "ca", "byoca-etcd")
	writeFakeCA(t, layout.OperatorDir, "front-proxy-ca", "byoca-fp")

	if err := Init(cfg, layout, "node-1"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	for _, p := range []string{
		"ca.crt",
		"etcd/ca.crt",
		"front-proxy-ca.crt",
	} {
		seed, err := os.ReadFile(filepath.Join(layout.OperatorDir, p))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(layout.PKIDir, p))
		if err != nil {
			t.Fatal(err)
		}
		if string(seed) != string(got) {
			t.Errorf("%s: not copied verbatim", p)
		}
	}

	// Verify leaves were actually signed by the seeded CAs (not regenerated).
	checks := []struct {
		leafPath string
		wantCN   string
	}{
		{filepath.Join(layout.PKIDir, "apiserver.crt"), "byoca-cluster"},
		{filepath.Join(layout.PKIDir, "etcd", "server.crt"), "byoca-etcd"},
		{filepath.Join(layout.PKIDir, "front-proxy-client.crt"), "byoca-fp"},
	}
	for _, c := range checks {
		got := parseCertIssuerCN(t, c.leafPath)
		if got != c.wantCN {
			t.Errorf("%s issuer CN = %q, want %q (seeded CA was not used as the signer)",
				c.leafPath, got, c.wantCN)
		}
	}
}

func parseCertIssuerCN(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("parseCertIssuerCN: no PEM block in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert.Issuer.CommonName
}

// crt-only seed (no matching key) is a configuration error: nanokube
// cannot sign leaves with a CA it has no key for, and silently
// self-signing would mask the operator's mistake.
func TestInitRejectsPartialSeed(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)
	if err := os.MkdirAll(layout.OperatorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.OperatorDir, "ca.crt"), []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Init(cfg, layout, "node-1")
	if err == nil {
		t.Fatal("expected error for crt-only seed, got nil")
	}
}
