package bootstraptoken

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCreate_MintsTokenSecretAndHash(t *testing.T) {
	client := fake.NewSimpleClientset()
	caPath := writeTestCA(t)

	res, err := Create(client, 24*time.Hour, caPath)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	parts := strings.SplitN(res.Token, ".", 2)
	if len(parts) != 2 || len(parts[0]) != 6 || len(parts[1]) != 16 {
		t.Fatalf("token format: got %q, want <6>.<16>", res.Token)
	}
	if !strings.HasPrefix(res.CACertHash, "sha256:") {
		t.Errorf("hash format: got %q", res.CACertHash)
	}
	sec, err := client.CoreV1().Secrets("kube-system").Get(context.Background(), "bootstrap-token-"+parts[0], metav1.GetOptions{})
	if err != nil {
		t.Fatalf("token secret: %v", err)
	}
	if string(sec.Type) != "bootstrap.kubernetes.io/token" {
		t.Errorf("secret type: got %q", sec.Type)
	}
	for _, k := range []string{"usage-bootstrap-authentication", "usage-bootstrap-signing"} {
		if string(sec.Data[k]) != "true" {
			t.Errorf("secret %s: got %q, want true", k, sec.Data[k])
		}
	}
}

func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
