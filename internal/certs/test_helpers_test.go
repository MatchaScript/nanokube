package certs

import (
	"path/filepath"
	"testing"

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
		OperatorDir:   filepath.Join(root, "etc-nanokube-certs"),
	}
}
