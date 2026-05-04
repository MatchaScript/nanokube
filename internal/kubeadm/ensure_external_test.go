package kubeadm_test

import (
	"os"
	"path/filepath"
	"testing"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/certs"
	"github.com/MatchaScript/nanokube/internal/kubeadm"
)

// Local testConfig + testLayout helpers — the unexported ones in
// ensure_test.go (package kubeadm) are not visible from the external
// test package. Duplicating ~20 lines of trivial setup is the standard
// Go cost of using `package foo_test` to break import cycles.
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

func testLayout(t *testing.T) kubeadm.Layout {
	t.Helper()
	root := t.TempDir()
	return kubeadm.Layout{
		PKIDir:        filepath.Join(root, "pki"),
		KubeconfigDir: filepath.Join(root, "kubernetes"),
		ManifestsDir:  filepath.Join(root, "kubernetes", "manifests"),
		KubeletDir:    filepath.Join(root, "var", "lib", "kubelet"),
	}
}

func certsLayout(l kubeadm.Layout) certs.Layout {
	return certs.Layout{
		PKIDir:        l.PKIDir,
		KubeconfigDir: l.KubeconfigDir,
	}
}

func TestEnsureProducesNonCertArtifacts(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)

	if err := certs.NewSigner(cfg, certsLayout(layout), "node-1").EnsureAll(); err != nil {
		t.Fatalf("certs.EnsureAll: %v", err)
	}
	if err := kubeadm.Ensure(cfg, layout, "node-1"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	checks := []string{
		filepath.Join(layout.ManifestsDir, "etcd.yaml"),
		filepath.Join(layout.ManifestsDir, "kube-apiserver.yaml"),
		filepath.Join(layout.ManifestsDir, "kube-controller-manager.yaml"),
		filepath.Join(layout.ManifestsDir, "kube-scheduler.yaml"),
		filepath.Join(layout.KubeletDir, "config.yaml"),
	}
	for _, p := range checks {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing artifact: %s (%v)", p, err)
		}
	}
}

// Ensure must NOT produce super-admin.conf — the system:masters-bound
// break-glass cred is created on demand by WriteSuperAdminKubeconfig
// only (during `nanokube init`) and removed immediately after the
// cluster-admins CRB is seeded. If Ensure were to recreate it, every
// reconcile boot would silently undo init's deletion.
func TestEnsureDoesNotProduceSuperAdminKubeconfig(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)
	if err := certs.NewSigner(cfg, certsLayout(layout), "node-1").EnsureAll(); err != nil {
		t.Fatalf("certs.EnsureAll: %v", err)
	}
	if err := kubeadm.Ensure(cfg, layout, "node-1"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	path := filepath.Join(layout.KubeconfigDir, "super-admin.conf")
	if _, err := os.Stat(path); err == nil {
		t.Errorf("Ensure unexpectedly produced %s — every reconcile boot would resurrect the break-glass cred", path)
	}
}

// WriteSuperAdminKubeconfig is the explicit, separate writer used by
// initialize.Run and by `nanokube kubeconfig super-admin`. Together with
// the test above it pins the contract: super-admin.conf only exists
// when something explicitly asks for it.
func TestWriteSuperAdminKubeconfigProducesFile(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)
	if err := certs.NewSigner(cfg, certsLayout(layout), "node-1").EnsureAll(); err != nil {
		t.Fatalf("certs.EnsureAll (prerequisite for CA): %v", err)
	}
	if err := kubeadm.WriteSuperAdminKubeconfig(cfg, layout, "node-1"); err != nil {
		t.Fatalf("WriteSuperAdminKubeconfig: %v", err)
	}
	path := filepath.Join(layout.KubeconfigDir, "super-admin.conf")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("WriteSuperAdminKubeconfig did not produce %s: %v", path, err)
	}
}
