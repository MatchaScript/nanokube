package certs

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNeedsRotation(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		notBefore time.Time
		notAfter  time.Time
		want      bool
	}{
		{
			name:      "short-lived fresh",
			notBefore: now.Add(-time.Hour),
			notAfter:  now.Add(365 * 24 * time.Hour),
			want:      false,
		},
		{
			name:      "short-lived near expiry (5mo left)",
			notBefore: now.Add(-7 * month),
			notAfter:  now.Add(5 * month),
			want:      true,
		},
		{
			name:      "short-lived just above threshold (8mo left)",
			notBefore: now.Add(-4 * month),
			notAfter:  now.Add(8 * month),
			want:      false,
		},
		{
			name:      "long-lived fresh",
			notBefore: now.Add(-30 * 24 * time.Hour),
			notAfter:  now.Add(10 * 365 * 24 * time.Hour),
			want:      false,
		},
		{
			name:      "long-lived near expiry (12mo left)",
			notBefore: now.Add(-9 * 365 * 24 * time.Hour),
			notAfter:  now.Add(12 * month),
			want:      true,
		},
		{
			name:      "long-lived just above threshold (20mo left)",
			notBefore: now.Add(-8 * 365 * 24 * time.Hour),
			notAfter:  now.Add(20 * month),
			want:      false,
		},
		{
			name:      "already expired",
			notBefore: now.Add(-365 * 24 * time.Hour),
			notAfter:  now.Add(-time.Hour),
			want:      true,
		},
		{
			// CR4: pre-NTP boot where realtime clock is behind cert
			// issuance. Silently rotating would mask the misconfigured
			// clock — instead, NeedsRotation returns false and the next
			// TLS handshake surfaces the wrong-clock error.
			name:      "not yet valid (clock-skew tolerant)",
			notBefore: now.Add(2 * time.Hour),
			notAfter:  now.Add(365 * 24 * time.Hour),
			want:      false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cert := &x509.Certificate{
				NotBefore: c.notBefore,
				NotAfter:  c.notAfter,
			}
			got := NeedsRotation(cert)
			if got != c.want {
				t.Errorf("NeedsRotation(%v..%v) = %v, want %v",
					c.notBefore, c.notAfter, got, c.want)
			}
		})
	}
}

func TestCheckCAsReportsAllThree(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	if err := NewSigner(cfg, layout).EnsureAll(); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}
	report, err := CheckCAs(layout)
	if err != nil {
		t.Fatalf("CheckCAs: %v", err)
	}
	for _, ca := range AllCAs() {
		exp, ok := report[ca]
		if !ok {
			t.Errorf("CA %s missing from report", ca)
			continue
		}
		if exp.NotFound {
			t.Errorf("CA %s reported NotFound after EnsureAll", ca)
			continue
		}
		if exp.Remaining <= 0 {
			t.Errorf("CA %s Remaining=%v, expected positive", ca, exp.Remaining)
		}
		if exp.Cert == nil {
			t.Errorf("CA %s Cert is nil", ca)
		}
	}
}

// Regenerating the cluster CA must (a) replace ca.{crt,key} with a
// distinct keypair, (b) re-issue every cluster-CA-signed PKI leaf and
// kubeconfig under the new issuer, and (c) leave the etcd CA and any
// etcd-signed material untouched.
func TestRegenerateCAClusterCascadesLeaves(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}

	clusterCAFingerprintBefore := certFingerprint(t, caCertPath(layout, CACluster))
	apiserverIssuerSerialBefore := certIssuerSerial(t, leafPath(layout, LeafAPIServer))
	apiserverKubeletIssuerBefore := certIssuerSerial(t, leafPath(layout, LeafAPIServerKubeletClient))
	etcdCAFingerprintBefore := certFingerprint(t, caCertPath(layout, CAEtcd))
	etcdServerFingerprintBefore := certFingerprint(t, leafPath(layout, LeafEtcdServer))
	adminConfBytesBefore := readFile(t, filepath.Join(layout.KubernetesDir, "admin.conf"))
	kubeletConfBytesBefore := readFile(t, filepath.Join(layout.KubernetesDir, "kubelet.conf"))

	if err := signer.RegenerateCA(CACluster); err != nil {
		t.Fatalf("RegenerateCA(CACluster): %v", err)
	}

	clusterCAFingerprintAfter := certFingerprint(t, caCertPath(layout, CACluster))
	if clusterCAFingerprintBefore == clusterCAFingerprintAfter {
		t.Error("cluster CA fingerprint unchanged after RegenerateCA(CACluster)")
	}

	apiserverIssuerSerialAfter := certIssuerSerial(t, leafPath(layout, LeafAPIServer))
	if apiserverIssuerSerialBefore == apiserverIssuerSerialAfter {
		t.Error("apiserver.crt issuer serial unchanged; was not re-signed by the new cluster CA")
	}
	apiserverKubeletIssuerAfter := certIssuerSerial(t, leafPath(layout, LeafAPIServerKubeletClient))
	if apiserverKubeletIssuerBefore == apiserverKubeletIssuerAfter {
		t.Error("apiserver-kubelet-client.crt issuer unchanged; was not re-signed")
	}

	for _, name := range []string{"admin.conf", "controller-manager.conf", "scheduler.conf", "kubelet.conf"} {
		path := filepath.Join(layout.KubernetesDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s missing after RegenerateCA: %v", name, err)
		}
	}
	if bytes.Equal(adminConfBytesBefore, readFile(t, filepath.Join(layout.KubernetesDir, "admin.conf"))) {
		t.Error("admin.conf unchanged after RegenerateCA(CACluster)")
	}
	if bytes.Equal(kubeletConfBytesBefore, readFile(t, filepath.Join(layout.KubernetesDir, "kubelet.conf"))) {
		t.Error("kubelet.conf unchanged after RegenerateCA(CACluster)")
	}

	if etcdCAFingerprintBefore != certFingerprint(t, caCertPath(layout, CAEtcd)) {
		t.Error("etcd CA fingerprint changed by cluster CA regeneration")
	}
	if etcdServerFingerprintBefore != certFingerprint(t, leafPath(layout, LeafEtcdServer)) {
		t.Error("etcd/server.crt changed by cluster CA regeneration")
	}
}

func TestRegenerateCAEtcdLeavesClusterAlone(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}

	etcdCABefore := certFingerprint(t, caCertPath(layout, CAEtcd))
	etcdServerIssuerBefore := certIssuerSerial(t, leafPath(layout, LeafEtcdServer))
	clusterCABefore := certFingerprint(t, caCertPath(layout, CACluster))
	apiserverBefore := certFingerprint(t, leafPath(layout, LeafAPIServer))
	adminBefore := readFile(t, filepath.Join(layout.KubernetesDir, "admin.conf"))

	if err := signer.RegenerateCA(CAEtcd); err != nil {
		t.Fatalf("RegenerateCA(CAEtcd): %v", err)
	}

	if etcdCABefore == certFingerprint(t, caCertPath(layout, CAEtcd)) {
		t.Error("etcd CA unchanged after RegenerateCA(CAEtcd)")
	}
	if etcdServerIssuerBefore == certIssuerSerial(t, leafPath(layout, LeafEtcdServer)) {
		t.Error("etcd/server.crt issuer unchanged; was not re-signed")
	}

	if clusterCABefore != certFingerprint(t, caCertPath(layout, CACluster)) {
		t.Error("cluster CA changed by etcd CA regeneration")
	}
	if apiserverBefore != certFingerprint(t, leafPath(layout, LeafAPIServer)) {
		t.Error("apiserver.crt changed by etcd CA regeneration")
	}
	if !bytes.Equal(adminBefore, readFile(t, filepath.Join(layout.KubernetesDir, "admin.conf"))) {
		t.Error("admin.conf changed by etcd CA regeneration")
	}
}

// Helpers

func certFingerprint(t *testing.T, path string) string {
	t.Helper()
	cert, err := parseCertFile(path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return cert.SerialNumber.String() + ":" + cert.Subject.String()
}

func certIssuerSerial(t *testing.T, path string) string {
	t.Helper()
	var cert *x509.Certificate
	var err error
	switch filepath.Ext(path) {
	case ".conf":
		cert, err = parseKubeconfigClientCert(path)
	default:
		cert, err = parseCertFile(path)
	}
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	// Serial of the issuer is exposed via AuthorityKeyId; we use the
	// issuer name + cert serial as a cheap "did this get re-signed"
	// signal — re-signing yields a new SerialNumber.
	return cert.SerialNumber.String() + ":" + cert.Issuer.String()
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
