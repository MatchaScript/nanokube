package kubeadm

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/MatchaScript/nanokube/internal/layouttest"
)

func writeKubeletConf(t *testing.T, path string, embedded bool) {
	t.Helper()
	c := clientcmdapi.NewConfig()
	c.Clusters["k"] = &clientcmdapi.Cluster{Server: "https://cp.example.test:6443"}
	a := &clientcmdapi.AuthInfo{}
	if embedded {
		a.ClientCertificateData = []byte("cert")
		a.ClientKeyData = []byte("key")
	}
	c.AuthInfos["default-auth"] = a
	c.Contexts["default"] = &clientcmdapi.Context{Cluster: "k", AuthInfo: "default-auth"}
	c.CurrentContext = "default"
	if err := clientcmd.WriteToFile(*c, path); err != nil {
		t.Fatalf("write kubelet.conf: %v", err)
	}
}

func TestFinalizeKubeletKubeconfig(t *testing.T) {
	l := layouttest.New(t)
	writeKubeletConf(t, l.KubeletKubeconfig, true)

	// No rotation store yet -> no-op.
	changed, err := FinalizeKubeletKubeconfig(l)
	if err != nil || changed {
		t.Fatalf("before pem: changed=%v err=%v, want false,nil", changed, err)
	}

	pem := filepath.Join(l.KubeletDir, "pki", "kubelet-client-current.pem")
	if err := os.MkdirAll(filepath.Dir(pem), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pem, []byte("pem"), 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err = FinalizeKubeletKubeconfig(l)
	if err != nil || !changed {
		t.Fatalf("first finalize: changed=%v err=%v, want true,nil", changed, err)
	}
	got, err := clientcmd.LoadFromFile(l.KubeletKubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	info := got.AuthInfos[got.Contexts[got.CurrentContext].AuthInfo]
	if info.ClientCertificate != pem || info.ClientKey != pem {
		t.Errorf("cert paths: got %q/%q, want %q", info.ClientCertificate, info.ClientKey, pem)
	}
	if len(info.ClientCertificateData) != 0 || len(info.ClientKeyData) != 0 {
		t.Errorf("embedded data not cleared")
	}

	// Idempotent.
	changed, err = FinalizeKubeletKubeconfig(l)
	if err != nil || changed {
		t.Fatalf("second finalize: changed=%v err=%v, want false,nil", changed, err)
	}
}

// A crash between the kubelet persisting its rotation store and writing
// kubelet.conf leaves "pem present, kubelet.conf absent" on a worker's
// next boot. Finalize must no-op so the kubelet can finish its
// bootstrap, not fail the boot.
func TestFinalizeKubeletKubeconfig_NoKubeletConfIsNoOp(t *testing.T) {
	l := layouttest.New(t)
	pem := filepath.Join(l.KubeletDir, "pki", "kubelet-client-current.pem")
	if err := os.MkdirAll(filepath.Dir(pem), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pem, []byte("pem"), 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := FinalizeKubeletKubeconfig(l)
	if err != nil || changed {
		t.Fatalf("missing kubelet.conf: changed=%v err=%v, want false,nil", changed, err)
	}
}
