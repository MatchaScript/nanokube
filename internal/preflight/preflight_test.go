package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatchaScript/nanokube/internal/ostree"
	"github.com/MatchaScript/nanokube/internal/paths"
	"github.com/MatchaScript/nanokube/internal/testutil"
)

// useFakeOSTreeMarker repoints ostree.OSTreeBootedMarker at a tempdir-
// controlled path and writes (or omits) the marker per `present`.
// Returns the marker path so the caller can mutate it mid-test.
func useFakeOSTreeMarker(t *testing.T, present bool) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ostree-booted")
	orig := ostree.OSTreeBootedMarker
	ostree.OSTreeBootedMarker = marker
	t.Cleanup(func() { ostree.OSTreeBootedMarker = orig })
	if present {
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return marker
}

// On a non-ostree system AllocateWorkspace runs gate checks but does
// not stage anything: UseBackups must be false, BackupTmp empty, and
// cleanup must be safely invocable as a no-op.
func TestAllocateWorkspace_NonOSTree_NoStaging(t *testing.T) {
	testutil.UseTempPaths(t)
	useFakeOSTreeMarker(t, false)

	ws, cleanup, err := AllocateWorkspace()
	if err != nil {
		t.Fatalf("AllocateWorkspace: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil; callers must always be able to defer it")
	}
	defer cleanup()

	if ws.UseBackups {
		t.Errorf("UseBackups = true on non-ostree system; want false")
	}
	if ws.BackupTmp != "" {
		t.Errorf("BackupTmp = %q on non-ostree; want empty", ws.BackupTmp)
	}
	if _, err := os.Stat(filepath.Join(paths.BackupsDir, stagingSubdir)); !os.IsNotExist(err) {
		t.Errorf("staging dir was created on non-ostree system: err=%v", err)
	}
}

// On an ostree system AllocateWorkspace stages an empty BackupTmp
// directory under BackupsDir and the cleanup wipes it.
func TestAllocateWorkspace_OSTree_StagesAndCleans(t *testing.T) {
	testutil.UseTempPaths(t)
	useFakeOSTreeMarker(t, true)

	ws, cleanup, err := AllocateWorkspace()
	if err != nil {
		t.Fatalf("AllocateWorkspace: %v", err)
	}
	if !ws.UseBackups {
		t.Fatal("UseBackups = false on ostree system; want true")
	}
	if ws.BackupTmp == "" {
		t.Fatal("BackupTmp empty on ostree system")
	}
	info, err := os.Stat(ws.BackupTmp)
	if err != nil {
		t.Fatalf("staging dir missing: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("staging is not a directory")
	}
	// Plant residue: the contract is that cleanup wipes whatever is
	// inside, regardless of how much partial copying happened.
	if err := os.WriteFile(filepath.Join(ws.BackupTmp, "halfway-copied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanup()
	if _, err := os.Stat(ws.BackupTmp); !os.IsNotExist(err) {
		t.Errorf("staging dir survived cleanup: err=%v", err)
	}
}

// Cleanup is a no-op once the staging dir has been renamed away (which
// is what backup.Create does on success). Tests this by running the
// rename ourselves and asserting cleanup does not error or recreate
// anything.
func TestAllocateWorkspace_CleanupAfterRenameIsNoop(t *testing.T) {
	testutil.UseTempPaths(t)
	useFakeOSTreeMarker(t, true)

	ws, cleanup, err := AllocateWorkspace()
	if err != nil {
		t.Fatalf("AllocateWorkspace: %v", err)
	}
	final := filepath.Join(paths.BackupsDir, "consumed")
	if err := os.Rename(ws.BackupTmp, final); err != nil {
		t.Fatalf("rename: %v", err)
	}

	cleanup() // must be a no-op; staging path no longer exists.

	if _, err := os.Stat(final); err != nil {
		t.Errorf("renamed dir disappeared: %v", err)
	}
	if _, err := os.Stat(ws.BackupTmp); !os.IsNotExist(err) {
		t.Errorf("cleanup recreated staging path: err=%v", err)
	}
}

// Residue from a previous interrupted boot must be wiped before the
// new staging is handed out, otherwise backup.Create's first writes
// could collide with stale files.
func TestAllocateWorkspace_ClearsStaleResidue(t *testing.T) {
	testutil.UseTempPaths(t)
	useFakeOSTreeMarker(t, true)

	if err := os.MkdirAll(paths.BackupsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(paths.BackupsDir, stagingSubdir)
	if err := os.MkdirAll(filepath.Join(stale, "leftover/etcd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "leftover/etcd/member"), []byte("from prior boot"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, cleanup, err := AllocateWorkspace()
	if err != nil {
		t.Fatalf("AllocateWorkspace: %v", err)
	}
	defer cleanup()

	entries, err := os.ReadDir(ws.BackupTmp)
	if err != nil {
		t.Fatalf("read staging: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("staging not empty after AllocateWorkspace: %v", entries)
	}
}

// A failing write probe must surface as an error AND return a callable
// (no-op) cleanup, so callers can use a uniform `defer cleanup()`
// pattern without nil-checking.
func TestAllocateWorkspace_WriteProbeFailureReturnsNoopCleanup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses mode bits")
	}
	root := testutil.UseTempPaths(t)
	useFakeOSTreeMarker(t, false)

	parent := filepath.Join(root, "var/lib")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	ws, cleanup, err := AllocateWorkspace()
	if err == nil {
		t.Fatal("AllocateWorkspace succeeded under unwritable NanoKubeVarDir")
	}
	if !strings.Contains(err.Error(), "write probe") {
		t.Errorf("expected write probe error, got: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil on error path; callers must always be able to defer it")
	}
	cleanup() // must not panic
	if ws.UseBackups {
		t.Errorf("zero-value Workspace.UseBackups = true on error path")
	}
}
