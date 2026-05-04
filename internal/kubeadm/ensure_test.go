package kubeadm

import (
	"path/filepath"
	"testing"

	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"k8s.io/kubernetes/cmd/kubeadm/app/componentconfigs"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
)

func testConfig() *v1alpha1.NanoKubeConfig {
	c := &v1alpha1.NanoKubeConfig{
		Metadata: v1alpha1.ObjectMeta{Name: "test"},
		Spec: v1alpha1.NanoKubeConfigSpec{
			ControlPlane: v1alpha1.ControlPlaneSpec{AdvertiseAddress: "192.168.10.10"},
			Certificates: v1alpha1.CertificatesSpec{
				SelfSigned: true,
				ExtraSANs:  []string{"nanokube.local", "10.0.0.5"},
			},
		},
	}
	v1alpha1.SetDefaults(c)
	return c
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

func TestBuildInitConfigurationPopulatesCoreFields(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)

	kc, err := BuildInitConfiguration(cfg, layout, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if kc.NodeRegistration.Name != "node-1" {
		t.Errorf("NodeRegistration.Name=%q, want node-1", kc.NodeRegistration.Name)
	}
	if kc.LocalAPIEndpoint.AdvertiseAddress != "192.168.10.10" {
		t.Errorf("AdvertiseAddress=%q", kc.LocalAPIEndpoint.AdvertiseAddress)
	}
	if kc.LocalAPIEndpoint.BindPort != 6443 {
		t.Errorf("BindPort=%d, want 6443", kc.LocalAPIEndpoint.BindPort)
	}
	if kc.Networking.ServiceSubnet != "10.96.0.0/12" {
		t.Errorf("ServiceSubnet=%q", kc.Networking.ServiceSubnet)
	}
	if kc.CertificatesDir != layout.PKIDir {
		t.Errorf("CertificatesDir=%q, want %q", kc.CertificatesDir, layout.PKIDir)
	}
	if kc.Etcd.Local == nil || len(kc.Etcd.Local.ServerCertSANs) != 2 {
		t.Errorf("Etcd.Local.ServerCertSANs=%v", kc.Etcd.Local)
	}
	if len(kc.APIServer.CertSANs) != 2 {
		t.Errorf("APIServer.CertSANs=%v, want 2 entries", kc.APIServer.CertSANs)
	}

	kubeletCfg := kc.ComponentConfigs[componentconfigs.KubeletGroup].Get().(*kubeletconfigv1beta1.KubeletConfiguration)
	if kubeletCfg.CgroupDriver != "systemd" {
		t.Errorf("KubeletConfiguration.CgroupDriver=%q, want systemd", kubeletCfg.CgroupDriver)
	}
	if len(kubeletCfg.ClusterDNS) != 1 || kubeletCfg.ClusterDNS[0] != "10.96.0.10" {
		t.Errorf("KubeletConfiguration.ClusterDNS=%v, want [10.96.0.10]", kubeletCfg.ClusterDNS)
	}

	if kc.CACertificateValidityPeriod == nil || kc.CACertificateValidityPeriod.Duration.Hours() != 3650*24 {
		t.Errorf("CACertificateValidityPeriod=%v, want 3650d", kc.CACertificateValidityPeriod)
	}
	if kc.CertificateValidityPeriod == nil || kc.CertificateValidityPeriod.Duration.Hours() != 365*24 {
		t.Errorf("CertificateValidityPeriod=%v, want 365d", kc.CertificateValidityPeriod)
	}
}
