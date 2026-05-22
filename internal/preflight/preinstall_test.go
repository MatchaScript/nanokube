package preflight

import (
	"os"
	"strings"
	"testing"

	"github.com/MatchaScript/nanokube/internal/paths"
	"github.com/MatchaScript/nanokube/internal/testutil"
)

// Preflight with useBackups=false only runs write probes; on a fresh
// temp tree both target dirs are creatable so it must succeed.
func TestPreflight_NoBackups_HappyPath(t *testing.T) {
	testutil.UseTempPaths(t)
	if err := Preflight(false); err != nil {
		t.Fatalf("Preflight(false) on fresh tmp paths: %v", err)
	}
}

// Surfacing EROFS / EACCES through the write probe is the whole point
// of this check. Drop write bits on the parent of NanoKubeVarDir and
// confirm Preflight reports it.
func TestPreflight_WriteProbeFails(t *testing.T) {
	root := testutil.UseTempPaths(t)
	parent := root + "/var/lib"
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses mode bits")
	}

	err := Preflight(false)
	if err == nil {
		t.Fatalf("Preflight(false) succeeded under unwritable %s", paths.NanoKubeVarDir)
	}
	if !strings.Contains(err.Error(), "write probe") {
		t.Fatalf("expected write-probe error, got: %v", err)
	}
}
