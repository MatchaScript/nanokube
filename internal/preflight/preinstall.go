package preflight

import (
	"fmt"
	"os"
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
)

// Preflight runs cheap filesystem sanity checks before the boot or init
// sequence performs work that has on-disk side effects. Goal: fail
// before kubeadm.Ensure / backup.Create / state writes start, so a
// transient disk-full or permission error does not cause greenboot to
// roll back a perfectly working cluster.
//
// useBackups gates the disk-space check (off on non-ostree systems
// where backup.Create is never invoked).
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
