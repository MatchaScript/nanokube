package kubeadm_test

import (
	"os"
	"strings"
	"testing"

	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	"github.com/MatchaScript/nanokube/internal/kubeadm"
	"github.com/MatchaScript/nanokube/internal/layouttest"
)

func TestEnsureWorker_WritesKubeletFilesOnly(t *testing.T) {
	l := layouttest.New(t)
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("defaulted config: %v", err)
	}
	cfg.NodeRegistration.Name = "test-node"
	cfg.NodeRegistration.CRISocket = "unix:///var/run/crio/crio.sock"
	if err := kubeadm.EnsureWorker(cfg, l); err != nil {
		t.Fatalf("EnsureWorker: %v", err)
	}
	for _, f := range []string{l.KubeletConfigFile, l.KubeletFlagsEnvFile} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("expected kubelet file %s: %v", f, err)
		}
	}
	entries, _ := os.ReadDir(l.ManifestsDir)
	if len(entries) != 0 {
		t.Errorf("worker must render no static pods, found %d entries", len(entries))
	}
}

// TestEnsureWorker_PinsResolvConfExplicitly mirrors internal/render's
// TestKubeletConfigResolvConfIsExplicit: the transitional on-disk path
// must pin resolvConf unconditionally too, so its output never depends
// on whether the host running it has systemd-resolved active.
func TestEnsureWorker_PinsResolvConfExplicitly(t *testing.T) {
	l := layouttest.New(t)
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("defaulted config: %v", err)
	}
	if err := kubeadm.EnsureWorker(cfg, l); err != nil {
		t.Fatalf("EnsureWorker: %v", err)
	}
	content, err := os.ReadFile(l.KubeletConfigFile)
	if err != nil {
		t.Fatalf("read kubelet config: %v", err)
	}
	want := "resolvConf: /run/systemd/resolve/resolv.conf"
	if !strings.Contains(string(content), want) {
		t.Errorf("kubelet config must pin %q explicitly (host state must not leak); got:\n%s",
			want, content)
	}
}
