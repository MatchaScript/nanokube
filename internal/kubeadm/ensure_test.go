package kubeadm

import (
	"path/filepath"
	"testing"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"
)

// testInitConfig returns a fully-defaulted *InitConfiguration with the
// nanokube-specific overrides tests rely on (advertise address,
// pinned k8s version, control-plane node name). Defaulting goes
// through kubeadm's own DefaultedStaticInitConfiguration so the
// fixture matches the shape config.Load would produce in production —
// in particular Etcd.Local is non-nil, which kubeadm phases require.
func testInitConfig(t *testing.T) *kubeadmapi.InitConfiguration {
	t.Helper()
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("DefaultedStaticInitConfiguration: %v", err)
	}
	cfg.NodeRegistration.Name = "test-node"
	cfg.NodeRegistration.CRISocket = "unix:///var/run/crio/crio.sock"
	cfg.LocalAPIEndpoint.AdvertiseAddress = "192.168.10.10"
	cfg.LocalAPIEndpoint.BindPort = 6443
	return cfg
}

func testLayout(t *testing.T) Layout {
	t.Helper()
	root := t.TempDir()
	return Layout{
		PKIDir:        filepath.Join(root, "pki"),
		KubeconfigDir: filepath.Join(root, "kubernetes"),
		ManifestsDir:  filepath.Join(root, "kubernetes", "manifests"),
		KubeletDir:    filepath.Join(root, "var", "lib", "kubelet"),
	}
}
