// Package ostree detects whether the running system is an ostree / bootc
// deployment and exposes the deployment ids that `rpm-ostree status`
// reports.
//
// Callers (lifecycle, backup) use deployment ids as backup name prefixes
// so that a restore after bootc rollback can pick the backup whose
// deployment id matches the (post-rollback) booted deployment. On
// non-ostree systems every function becomes a no-op and the caller is
// expected to skip backup/restore entirely.
package ostree

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// OSTreeBootedMarker is the kernel-provided file that ostree / rpm-ostree
// touches when the system booted an ostree deployment. Declared as a var
// so tests can point it at a controlled location.
var OSTreeBootedMarker = "/run/ostree-booted"

// IsOSTree reports whether we are running on an ostree / bootc deployment.
// When false, callers should skip backup/restore and other deployment-id
// based logic.
func IsOSTree() (bool, error) {
	_, err := os.Stat(OSTreeBootedMarker)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", OSTreeBootedMarker, err)
}

type deployment struct {
	ID     string `json:"id"`
	Booted bool   `json:"booted"`
}

// BootedDeploymentID returns the id of the currently-booted ostree
// deployment. Returns ("", nil) on non-ostree systems so callers can
// branch on emptiness.
func BootedDeploymentID() (string, error) {
	ok, err := IsOSTree()
	if err != nil || !ok {
		return "", err
	}
	deps, err := allDeployments()
	if err != nil {
		return "", err
	}
	for _, d := range deps {
		if d.Booted {
			return d.ID, nil
		}
	}
	return "", fmt.Errorf("no booted deployment in rpm-ostree status output")
}

// AllDeploymentIDs returns every deployment id currently on the system
// (booted, pending, rollback). Used by the backup pruner to drop backups
// whose deployment has been garbage-collected by bootc.
func AllDeploymentIDs() ([]string, error) {
	ok, err := IsOSTree()
	if err != nil || !ok {
		return nil, err
	}
	deps, err := allDeployments()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(deps))
	for _, d := range deps {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

func allDeployments() ([]deployment, error) {
	cmd := exec.Command("rpm-ostree", "status", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rpm-ostree status: %v: %s", err, stderr.String())
	}
	var status struct {
		Deployments []deployment `json:"deployments"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		return nil, fmt.Errorf("parse rpm-ostree status: %w", err)
	}
	return status.Deployments, nil
}
