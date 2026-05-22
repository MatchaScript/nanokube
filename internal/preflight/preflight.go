package preflight

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/MatchaScript/nanokube/internal/ostree"
	"github.com/MatchaScript/nanokube/internal/paths"
)

// stagingSubdir is the well-known dirname under BackupsDir used as the
// pre-rename scratch area for backup.Create. AllocateWorkspace clears
// any prior residue at this path before each boot, so a previous
// boot's interrupted Create cannot corrupt the next one.
const stagingSubdir = ".staging"

// Workspace is the host-side scratch area allocated up-front, before
// boot performs any operation with on-disk side effects. backup.Create
// writes inside Workspace.BackupTmp; on success it renames that directory
// to its final location, on failure the deferred cleanup removes whatever
// remains. Cleanup ownership is the boot orchestrator's, not backup's:
// backup.Create returns an error and stops, the caller's defer cleanup()
// guarantees no half-written staging is left behind.
type Workspace struct {
	// UseBackups is true on ostree/bootc systems where backup.Create is
	// meaningful. On non-ostree systems the boot orchestrator skips
	// backup work entirely; BackupTmp is empty in that case and the
	// returned cleanup is a no-op.
	UseBackups bool

	// BackupTmp is the staging directory backup.Create writes its
	// snapshot tree into before renaming to the final backup name.
	// Empty when UseBackups is false.
	BackupTmp string
}

// AllocateWorkspace gates on-disk readiness AND stages the scratch
// directory the boot flow will write into. Returns a cleanup function
// the caller MUST defer. The cleanup is a no-op once backup.Create
// successfully renames the staging dir to its final name (rename
// destroys the staging path; RemoveAll on a missing path is benign).
//
// The returned cleanup is always safe to call exactly once. On gate
// failure no allocation has happened and cleanup is still callable
// (no-op) so callers can use a uniform `defer cleanup()` pattern.
func AllocateWorkspace() (Workspace, func(), error) {
	noop := func() {}

	for _, dir := range []string{paths.NanoKubeVarDir, paths.KubernetesDir} {
		if err := writeProbe(dir); err != nil {
			return Workspace{}, noop, fmt.Errorf("write probe %s: %w", dir, err)
		}
	}

	isOstree, err := ostree.IsOSTree()
	if err != nil {
		return Workspace{}, noop, fmt.Errorf("detect ostree: %w", err)
	}
	if !isOstree {
		return Workspace{UseBackups: false}, noop, nil
	}

	if err := checkBackupSpace(); err != nil {
		return Workspace{}, noop, err
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
	return Workspace{UseBackups: true, BackupTmp: staging}, cleanup, nil
}
