package ostree

import (
	"os"
	"path/filepath"
	"testing"
)

// withMarker points OSTreeBootedMarker at a path under t.TempDir() and
// registers a Cleanup that restores the original. The marker file itself
// is created by the caller via the returned path.
func withMarker(t *testing.T) string {
	t.Helper()
	orig := OSTreeBootedMarker
	p := filepath.Join(t.TempDir(), "ostree-booted")
	OSTreeBootedMarker = p
	t.Cleanup(func() { OSTreeBootedMarker = orig })
	return p
}

func TestIsOSTree_FalseWhenMarkerAbsent(t *testing.T) {
	withMarker(t)
	ok, err := IsOSTree()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("IsOSTree true without marker")
	}
}

func TestIsOSTree_TrueWhenMarkerPresent(t *testing.T) {
	marker := withMarker(t)
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := IsOSTree()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("IsOSTree false with marker present")
	}
}

// BootedDeploymentID / AllDeploymentIDs short-circuit to ("", nil) on
// non-ostree systems so downstream callers can treat empty deploymentID
// as "skip backup". Guard the contract.
func TestDeploymentIDs_NoOpOnNonOSTree(t *testing.T) {
	withMarker(t) // marker absent = non-ostree

	id, err := BootedDeploymentID()
	if err != nil {
		t.Errorf("BootedDeploymentID on non-ostree = err=%v", err)
	}
	if id != "" {
		t.Errorf("BootedDeploymentID on non-ostree = %q; want empty", id)
	}

	ids, err := AllDeploymentIDs()
	if err != nil {
		t.Errorf("AllDeploymentIDs on non-ostree = err=%v", err)
	}
	if len(ids) != 0 {
		t.Errorf("AllDeploymentIDs on non-ostree = %v; want empty", ids)
	}
}
