package certs

import "testing"

func TestAllLeavesIncludesPKIAndKubeconfigButNotKubelet(t *testing.T) {
	got := AllLeaves()
	want := map[LeafKind]bool{
		LeafAPIServer:              true,
		LeafAPIServerKubeletClient: true,
		LeafAPIServerEtcdClient:    true,
		LeafFrontProxyClient:       true,
		LeafEtcdServer:             true,
		LeafEtcdPeer:               true,
		LeafEtcdHealthcheckClient:  true,
		LeafAdminConf:              true,
		LeafSuperAdminConf:         true,
		LeafControllerManagerConf:  true,
		LeafSchedulerConf:          true,
	}
	if len(got) != len(want) {
		t.Fatalf("AllLeaves() length=%d, want %d", len(got), len(want))
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected leaf in AllLeaves: %q", k)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("missing leaf in AllLeaves: %q", k)
	}
}

// kubelet.conf is intentionally NOT a LeafKind: kubelet itself rotates
// its client cert via CSR. Mirroring that exclusion in our type system
// makes the no-touch policy structural rather than a runtime check.
func TestLeafKindStringDoesNotIncludeKubeletConf(t *testing.T) {
	for _, k := range AllLeaves() {
		if string(k) == "kubelet.conf" {
			t.Fatalf("LeafKind %q should not exist; kubelet.conf is delegated to kubelet", k)
		}
	}
}
