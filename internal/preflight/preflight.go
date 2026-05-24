package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/MatchaScript/nanokube/internal/backup"
	"github.com/MatchaScript/nanokube/internal/paths"
)

const (
	// MinFreeBytes is the floor for free space on the filesystem hosting
	// /var/lib/nanokube/backups. 2 GiB covers a small etcd snapshot plus
	// /etc/kubernetes with comfortable headroom; state files alone are
	// KB-scale and not what this check guards against.
	MinFreeBytes uint64 = 2 * 1024 * 1024 * 1024

	// BackupHeadroomFactor scales the most recent backup's size to set
	// the headroom requirement for the next snapshot. 1.5× lets etcd
	// data grow modestly between boots without tripping the gate.
	BackupHeadroomFactor = 1.5

	// stagingSubdir is the well-known dirname under BackupsDir used as the
	// pre-rename scratch area for backup.Create. AllocateWorkspace clears
	// any prior residue at this path before each boot, so a previous
	// boot's interrupted Create cannot corrupt the next one.
	stagingSubdir = ".staging"
)

// Preflight runs cheap filesystem sanity checks shared by `nanokube init`
// and `nanokube boot`. Goal: fail before kubeadm.Ensure / backup.Create /
// state writes start, so a transient disk-full or permission error does
// not cause greenboot to roll back a perfectly working cluster.
//
// useBackups gates the disk-space check (off on non-ostree systems where
// backup.Create is never invoked).
func Preflight(useBackups bool) error {
	for _, dir := range []string{paths.NanoKubeVarDir, paths.KubernetesDir} {
		if err := writeProbe(dir); err != nil {
			return fmt.Errorf("write probe %s: %w", dir, err)
		}
	}
	if useBackups {
		if err := checkBackupSpace(); err != nil {
			return err
		}
	}
	return nil
}

// writeProbe creates a temp file in dir and removes it. Surfaces
// permission, EROFS, and missing-parent failures without depending on
// any production write reaching them first.
func writeProbe(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".preflight-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func checkBackupSpace() error {
	if err := os.MkdirAll(paths.BackupsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", paths.BackupsDir, err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(paths.BackupsDir, &st); err != nil {
		return fmt.Errorf("statfs %s: %w", paths.BackupsDir, err)
	}
	free := uint64(st.Bavail) * uint64(st.Bsize)

	if free < MinFreeBytes {
		return fmt.Errorf("%s: free %d bytes below minimum %d", paths.BackupsDir, free, MinFreeBytes)
	}

	latest, err := backup.LatestSize()
	if err != nil {
		return fmt.Errorf("read latest backup size: %w", err)
	}
	needed := uint64(float64(latest) * BackupHeadroomFactor)
	if free < needed {
		return fmt.Errorf("%s: free %d bytes below %d (1.5×latest backup %d)",
			paths.BackupsDir, free, needed, latest)
	}
	return nil
}

// Workspace is the host-side scratch area allocated up-front, before
// boot performs any operation with on-disk side effects. backup.Create
// writes inside Workspace.BackupTmp; on success it renames that directory
// to its final location, on failure the deferred cleanup removes whatever
// remains. Cleanup ownership is the boot orchestrator's, not backup's:
// backup.Create returns an error and stops, the caller's defer cleanup()
// guarantees no half-written staging is left behind.
type Workspace struct {
	// BackupTmp is the staging directory backup.Create writes its
	// snapshot tree into before renaming to the final backup name.
	// Empty when AllocateWorkspace was called with useBackups=false.
	BackupTmp string
}

// AllocateWorkspace stages the scratch directory backup.Create writes
// into. It does NOT re-run the shared Preflight gate; callers MUST have
// already called Preflight before reaching here. useBackups=false
// short-circuits to a zero Workspace and a no-op cleanup, so callers on
// non-ostree systems can keep the same `defer cleanup()` pattern.
//
// The returned cleanup is always safe to call exactly once. The cleanup
// becomes a benign no-op once backup.Create renames the staging dir to
// its final name (rename destroys the staging path; RemoveAll on a
// missing path is benign).
func AllocateWorkspace(useBackups bool) (Workspace, func(), error) {
	noop := func() {}
	if !useBackups {
		return Workspace{}, noop, nil
	}

	if err := os.MkdirAll(paths.BackupsDir, 0o700); err != nil {
		return Workspace{}, noop, fmt.Errorf("mkdir %s: %w", paths.BackupsDir, err)
	}
	staging := filepath.Join(paths.BackupsDir, stagingSubdir)
	// Wipe any residue from a previously-interrupted boot before handing
	// the staging dir to backup.Create. This is the only place residue
	// survives across boots — once we cleanup at the end of a boot, the
	// next boot's AllocateWorkspace clears anything that slipped through.
	if err := os.RemoveAll(staging); err != nil {
		return Workspace{}, noop, fmt.Errorf("clear stale staging %s: %w", staging, err)
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return Workspace{}, noop, fmt.Errorf("mkdir staging %s: %w", staging, err)
	}

	cleanup := func() {
		_ = os.RemoveAll(staging)
	}
	return Workspace{BackupTmp: staging}, cleanup, nil
}
