package preflight

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MatchaScript/nanokube/internal/paths"
	"github.com/MatchaScript/nanokube/internal/testutil"
)

// With useBackups=false AllocateWorkspace must not touch the filesystem
// and must hand back a no-op cleanup so callers can defer uniformly.
func TestAllocateWorkspace_NoBackups_NoStaging(t *testing.T) {
	testutil.UseTempPaths(t)

	ws, cleanup, err := AllocateWorkspace(false)
	if err != nil {
		t.Fatalf("AllocateWorkspace(false): %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil; callers must always be able to defer it")
	}
	defer cleanup()

	if ws.BackupTmp != "" {
		t.Errorf("BackupTmp = %q with useBackups=false; want empty", ws.BackupTmp)
	}
	if _, err := os.Stat(filepath.Join(paths.BackupsDir, stagingSubdir)); !os.IsNotExist(err) {
		t.Errorf("staging dir was created with useBackups=false: err=%v", err)
	}
}

// With useBackups=true AllocateWorkspace stages an empty BackupTmp
// directory under BackupsDir and the cleanup wipes it.
func TestAllocateWorkspace_Backups_StagesAndCleans(t *testing.T) {
	testutil.UseTempPaths(t)

	ws, cleanup, err := AllocateWorkspace(true)
	if err != nil {
		t.Fatalf("AllocateWorkspace(true): %v", err)
	}
	if ws.BackupTmp == "" {
		t.Fatal("BackupTmp empty with useBackups=true")
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

	ws, cleanup, err := AllocateWorkspace(true)
	if err != nil {
		t.Fatalf("AllocateWorkspace(true): %v", err)
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

	ws, cleanup, err := AllocateWorkspace(true)
	if err != nil {
		t.Fatalf("AllocateWorkspace(true): %v", err)
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
