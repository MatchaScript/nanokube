package addnode

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmapiv1 "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/v1beta4"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/config"
	"github.com/MatchaScript/nanokube/internal/layouttest"
)

func TestNormalizeServer(t *testing.T) {
	for _, tc := range []struct{ in, wantURL, wantHostPort, wantHost string }{
		{"https://10.0.2.10:6443", "https://10.0.2.10:6443", "10.0.2.10:6443", "10.0.2.10"},
		{"10.0.2.10:6443", "https://10.0.2.10:6443", "10.0.2.10:6443", "10.0.2.10"},
	} {
		u, hp, h, err := normalizeServer(tc.in)
		if err != nil || u != tc.wantURL || hp != tc.wantHostPort || h != tc.wantHost {
			t.Errorf("normalizeServer(%q) = %q,%q,%q,%v; want %q,%q,%q,nil", tc.in, u, hp, h, err, tc.wantURL, tc.wantHostPort, tc.wantHost)
		}
	}
}

func TestRun_NoOpWhenAlreadyJoined(t *testing.T) {
	l := layouttest.New(t)
	if err := os.MkdirAll(filepath.Dir(l.KubeletKubeconfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.KubeletKubeconfig, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), Options{Server: "https://10.0.2.10:6443", Token: "abcdef.0123456789abcdef"}, l, "v1.35.0", io.Discard)
	if err != nil {
		t.Fatalf("already-joined must be a no-op, got %v", err)
	}
}

// TestMarshalJoin_WriteReadContract is add-node's side of the contract
// with Boot's read: build the JoinConfiguration the way Run does (kubeadm
// defaulter + file-discovery swap to the layout's kubelet.conf), marshal
// it with a distinctive ClusterConfiguration, write it to the layout's
// ConfigFile, then Load it back and assert HasJoin and the
// controlPlaneEndpoint survive.
func TestMarshalJoin_WriteReadContract(t *testing.T) {
	l := layouttest.New(t)

	base, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("DefaultedStaticInitConfiguration: %v", err)
	}
	clusterCfg := base.ClusterConfiguration.DeepCopy()
	clusterCfg.KubernetesVersion = "v1.35.0"
	clusterCfg.ControlPlaneEndpoint = "cp.example.test:6443"

	// DefaultedJoinConfiguration validates the file-discovery path on
	// disk, so it must exist; SkipCRIDetect avoids the host CRI probe.
	kubeconfigPath := filepath.Join(t.TempDir(), "kubelet.conf")
	if err := os.WriteFile(kubeconfigPath, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	joinCfg, err := kubeadmconfig.DefaultedJoinConfiguration(
		&kubeadmapiv1.JoinConfiguration{
			Discovery: kubeadmapiv1.Discovery{
				File: &kubeadmapiv1.FileDiscovery{KubeConfigPath: kubeconfigPath},
			},
		},
		kubeadmconfig.LoadOrDefaultConfigurationOptions{SkipCRIDetect: true},
	)
	if err != nil {
		t.Fatalf("DefaultedJoinConfiguration: %v", err)
	}

	// Mirror Run: swap discovery to file-discovery at the layout's
	// kubelet.conf so the persisted document carries no bootstrap token.
	persistJoin := joinCfg.DeepCopy()
	persistJoin.Discovery = kubeadmapi.Discovery{
		File: &kubeadmapi.FileDiscovery{KubeConfigPath: l.KubeletKubeconfig},
	}

	data, err := config.MarshalJoin(v1alpha1.NewDefault(), persistJoin, clusterCfg)
	if err != nil {
		t.Fatalf("MarshalJoin: %v", err)
	}
	if err := os.MkdirAll(l.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.ConfigFile, data, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := config.Load(l.ConfigFile, l)
	if err != nil {
		t.Fatalf("Load after MarshalJoin: %v\n--- marshalled ---\n%s", err, data)
	}
	if !loaded.HasJoin {
		t.Error("HasJoin: got false, want true after MarshalJoin round-trip")
	}
	if loaded.Init.ControlPlaneEndpoint != "cp.example.test:6443" {
		t.Errorf("controlPlaneEndpoint round-trip: got %q", loaded.Init.ControlPlaneEndpoint)
	}
}
