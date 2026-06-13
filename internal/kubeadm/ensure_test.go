package kubeadm_test

import (
	"os"
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
