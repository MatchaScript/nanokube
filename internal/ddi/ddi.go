// Package ddi builds a confext DDI (a systemd Discoverable Disk Image,
// ".raw") from a rendered file list by shelling out to systemd-repart.
// The DDI is the on-wire/on-disk config artifact nanokube distributes:
// nanokube-agent places it at /var/lib/confexts/<name>.raw and merges it
// into /etc with `systemd-confext refresh`.
package ddi

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/MatchaScript/nanokube/internal/render"
)

// ErrSystemdRepartNotFound is returned by Build when systemd-repart is
// not present in PATH. Callers (and tests) match on it with errors.Is
// to distinguish "tool missing" from a genuine build failure.
var ErrSystemdRepartNotFound = errors.New("systemd-repart not found in PATH")

// extensionReleaseDir is the confext-tree-relative directory holding the
// extension-release marker file. Confext's built-in CopyFiles=/etc/
// partition rule only pulls from <copy-source>/etc/, so — unlike
// sysext's usr/lib/extension-release.d/ convention — this must be
// nested under etc/, not usr/lib/.
const extensionReleaseDir = "etc/extension-release.d"

// extensionReleaseSysextLevel is written as SYSEXT_LEVEL= in every
// confext's extension-release file. Confirmed against a real Fedora 44
// bootc host (systemd 259): an extension-release carrying only ID= (no
// SYSEXT_LEVEL, no VERSION_ID) is not treated as "no version check" —
// systemd-confext refresh instead logs "does not contain VERSION_ID in
// release file but requested to match '44'" and silently drops the
// extension. SYSEXT_LEVEL is systemd's mechanism for a confext
// version-compatibility marker independent of the host's own VERSION_ID
// (which changes every OS release and is otherwise unrelated to confext
// content compatibility) — see test/devenv/image/Containerfile, which
// declares a matching SYSEXT_LEVEL in the node image's /etc/os-release.
//
// This alone is NOT sufficient for delivery before a node has actually
// booted an image build carrying that os-release line: a node running
// an older image (or, in Step 1, one that never reboots at all) still
// checks against whatever /etc/os-release it currently has booted, so
// the agent's systemd-confext refresh also passes --force ("ignore
// version incompatibilities") to make delivery work independent of
// image staging/reboot, matching the architecture's no-reboot-required
// design (see agent/src/ops.rs's refresh, and
// docs/nanokube/2026-07-06-step1-implementation-plan-rev5.md). Once a
// node has booted an image whose os-release matches this SYSEXT_LEVEL,
// matching is exact even without --force; keeping this line is what
// makes that eventually true instead of confexts depending on --force
// forever.
const extensionReleaseSysextLevel = "1"

// BuildInput specifies what to bake into a confext DDI.
type BuildInput struct {
	// Name is the confext version name (typically render.Desired.Name()).
	// It becomes the extension-release filename suffix.
	Name string

	// ExtensionReleaseID is written as ID=<value> in the confext's
	// extension-release file, matched against the host's /etc/os-release
	// ID at merge time (systemd-confext refresh).
	ExtensionReleaseID string

	// Files are the confext-tree-relative files to bake in (e.g. from
	// render.Desired.Files). Must not include an extension-release entry
	// — Build synthesizes that itself.
	Files []render.File

	// PrivateKeyPath and CertificatePath enable dm-verity signing when
	// both are set. Zero value produces an unsigned DDI (v0 default per
	// architecture rev5 — signing is opt-in).
	PrivateKeyPath  string
	CertificatePath string
}

// Build renders input into a confext DDI (.raw) at outputPath by
// shelling out to systemd-repart --make-ddi=confext --offline. Building
// is unprivileged; the caller is responsible for computing outputPath's
// sha256 afterward if needed (Build doesn't duplicate that).
func Build(input BuildInput, outputPath string) error {
	if _, err := exec.LookPath("systemd-repart"); err != nil {
		return ErrSystemdRepartNotFound
	}

	tree, err := os.MkdirTemp("", "nanokube-ddi-*")
	if err != nil {
		return fmt.Errorf("ddi: scratch tree: %w", err)
	}
	defer os.RemoveAll(tree)

	for _, f := range input.Files {
		if err := writeTreeFile(tree, f); err != nil {
			return err
		}
	}

	release := render.File{
		Path:    filepath.Join(extensionReleaseDir, "extension-release."+input.Name),
		Content: extensionReleaseContent(input.ExtensionReleaseID),
	}
	if err := writeTreeFile(tree, release); err != nil {
		return err
	}

	signed := input.PrivateKeyPath != "" && input.CertificatePath != ""

	var args []string
	if signed {
		args = append(args, "--private-key="+input.PrivateKeyPath, "--certificate="+input.CertificatePath)
	}
	args = append(args, "--make-ddi=confext", "--offline=yes")
	if !signed {
		// --make-ddi=confext's built-in partition template always
		// includes a root-verity-sig partition, which errors unless a
		// key is provided or the partition is explicitly excluded.
		// This is how an unsigned v0 build opts out of signing.
		args = append(args, "--exclude-partitions=root-verity-sig")
	}
	args = append(args, "--copy-source="+tree, outputPath)

	cmd := exec.Command("systemd-repart", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ddi: %s: %w: %s", cmd.String(), err, out.String())
	}
	return nil
}

// extensionReleaseContent returns a confext extension-release file's
// content for the given ID (see extensionReleaseSysextLevel for why
// SYSEXT_LEVEL is included alongside it).
func extensionReleaseContent(id string) []byte {
	return []byte("ID=" + id + "\nSYSEXT_LEVEL=" + extensionReleaseSysextLevel + "\n")
}

// writeTreeFile writes f under tree, creating parent directories as
// needed and defaulting to mode 0o644 when f.Mode is unset.
func writeTreeFile(tree string, f render.File) error {
	path := filepath.Join(tree, f.Path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ddi: mkdir %s: %w", filepath.Dir(path), err)
	}
	mode := f.Mode
	if mode == 0 {
		mode = 0o644
	}
	if err := os.WriteFile(path, f.Content, mode); err != nil {
		return fmt.Errorf("ddi: write %s: %w", path, err)
	}
	return nil
}
