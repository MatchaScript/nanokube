package certs

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

func TestEnsureAllProducesPKIAndKubeconfigsFromScratch(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)

	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}

	// PKI files (kubeadm certs.CreatePKIAssets canonical output)
	pkiFiles := []string{
		"ca.crt", "ca.key",
		"apiserver.crt", "apiserver.key",
		"apiserver-kubelet-client.crt", "apiserver-kubelet-client.key",
		"apiserver-etcd-client.crt", "apiserver-etcd-client.key",
		"front-proxy-ca.crt", "front-proxy-ca.key",
		"front-proxy-client.crt", "front-proxy-client.key",
		"etcd/ca.crt", "etcd/ca.key",
		"etcd/server.crt", "etcd/server.key",
		"etcd/peer.crt", "etcd/peer.key",
		"etcd/healthcheck-client.crt", "etcd/healthcheck-client.key",
		"sa.key", "sa.pub",
	}
	for _, f := range pkiFiles {
		if _, err := os.Stat(filepath.Join(layout.PKIDir, f)); err != nil {
			t.Errorf("missing PKI artifact %s: %v", f, err)
		}
	}
}

// EnsureAll must NOT produce super-admin.conf — the system:masters cred
// is created on demand by initialize.WriteSuperAdminKubeconfig only.
// (Mirrors the assertion in kubeadm/ensure_test.go's
// TestEnsureDoesNotProduceSuperAdminKubeconfig.)
func TestEnsureAllDoesNotProduceSuperAdminKubeconfig(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.PKIDir, "super-admin.conf")); err == nil {
		t.Error("EnsureAll unexpectedly produced super-admin.conf in PKIDir")
	}
}

func TestEnsureAllIsIdempotent(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(layout.PKIDir, "apiserver.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.EnsureAll(); err != nil {
		t.Fatalf("second EnsureAll: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(layout.PKIDir, "apiserver.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("apiserver.crt rotated by second EnsureAll; phase certs should reuse existing valid certs")
	}
}

func parseCertSerial(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("no PEM block in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert.SerialNumber.String()
}

func TestRenewLeavesRotatesTargetButNotCA(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}

	apiBefore := parseCertSerial(t, filepath.Join(layout.PKIDir, "apiserver.crt"))
	caBefore := parseCertSerial(t, filepath.Join(layout.PKIDir, "ca.crt"))

	if err := signer.RenewLeaves([]LeafKind{LeafAPIServer}); err != nil {
		t.Fatalf("RenewLeaves: %v", err)
	}

	apiAfter := parseCertSerial(t, filepath.Join(layout.PKIDir, "apiserver.crt"))
	caAfter := parseCertSerial(t, filepath.Join(layout.PKIDir, "ca.crt"))

	if apiBefore == apiAfter {
		t.Error("apiserver.crt serial unchanged after RenewLeaves; renewal did not rotate the cert")
	}
	if caBefore != caAfter {
		t.Errorf("ca.crt serial changed: before=%s after=%s; CA must NOT be rotated by RenewLeaves",
			caBefore, caAfter)
	}
}

func TestRenewLeavesLeavesUntargetedCertsAlone(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}

	otherBefore := parseCertSerial(t, filepath.Join(layout.PKIDir, "etcd", "server.crt"))
	if err := signer.RenewLeaves([]LeafKind{LeafAPIServer}); err != nil {
		t.Fatal(err)
	}
	otherAfter := parseCertSerial(t, filepath.Join(layout.PKIDir, "etcd", "server.crt"))
	if otherBefore != otherAfter {
		t.Errorf("etcd/server.crt serial changed: before=%s after=%s; only the targeted leaf should rotate",
			otherBefore, otherAfter)
	}
}

// kubeconfigEmbeddedCertSerial parses the kubeconfig at path, extracts the
// client certificate embedded in the first AuthInfo entry, and returns its
// serial number as a string.  Any failure is a precondition violation and
// causes an immediate t.Fatal.
func kubeconfigEmbeddedCertSerial(t *testing.T, path string) string {
	t.Helper()
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatalf("kubeconfigEmbeddedCertSerial: load %s: %v", path, err)
	}
	if len(cfg.AuthInfos) == 0 {
		t.Fatalf("kubeconfigEmbeddedCertSerial: no AuthInfos in %s", path)
	}
	// admin.conf has exactly one user entry.
	var certData []byte
	for _, info := range cfg.AuthInfos {
		if len(info.ClientCertificateData) == 0 {
			t.Fatalf("kubeconfigEmbeddedCertSerial: AuthInfo has no client-certificate-data in %s", path)
		}
		certData = info.ClientCertificateData
		break
	}
	// ClientCertificateData is already the raw PEM bytes (clientcmd stores it
	// decoded from the base64 in the YAML).  If for some reason it arrives
	// base64-encoded, fall back gracefully.
	pemBytes := certData
	if block, _ := pem.Decode(certData); block == nil {
		// Not PEM — try base64 decode.
		decoded, decErr := base64.StdEncoding.DecodeString(string(certData))
		if decErr != nil {
			t.Fatalf("kubeconfigEmbeddedCertSerial: data in %s is neither PEM nor base64: %v", path, decErr)
		}
		pemBytes = decoded
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatalf("kubeconfigEmbeddedCertSerial: no PEM block in cert data from %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("kubeconfigEmbeddedCertSerial: parse cert from %s: %v", path, err)
	}
	return cert.SerialNumber.String()
}

func TestRenewLeavesCoversKubeconfigEmbeddedCert(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}

	adminPath := filepath.Join(layout.KubernetesDir, "admin.conf")
	serialBefore := kubeconfigEmbeddedCertSerial(t, adminPath)

	if err := signer.RenewLeaves([]LeafKind{LeafAdminConf}); err != nil {
		t.Fatalf("RenewLeaves(admin.conf): %v", err)
	}

	serialAfter := kubeconfigEmbeddedCertSerial(t, adminPath)
	if serialBefore == serialAfter {
		t.Errorf("admin.conf embedded cert serial unchanged (%s) after RenewLeaves(LeafAdminConf); kubeconfig cert was not rotated", serialBefore)
	}
}
