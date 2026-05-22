package kubeadm

import "testing"

// DefaultLayout must point at the canonical kubeadm paths; operators
// and packaging rely on these locations.
func TestDefaultLayout_PointsAtAbsoluteKubeadmPaths(t *testing.T) {
	l := DefaultLayout()
	if l.PKIDir == "" || l.KubeconfigDir == "" || l.ManifestsDir == "" || l.KubeletDir == "" {
		t.Fatalf("DefaultLayout has empty fields: %+v", l)
	}
	for _, p := range []string{l.PKIDir, l.KubeconfigDir, l.ManifestsDir, l.KubeletDir} {
		if p[0] != '/' {
			t.Errorf("not absolute: %q", p)
		}
	}
}
