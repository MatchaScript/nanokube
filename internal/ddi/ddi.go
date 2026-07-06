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

// BuildInput specifies what to bake into a confext DDI.
type BuildInput struct {
	// Name is the confext version name (typically render.Desired.Name()).
	// It becomes the extension-release filename suffix.
	Name string

	// ExtensionReleaseID no longer affects the built DDI: every
	// nanokube confext's extension-release file unconditionally declares
	// ID=_any (see extensionReleaseContent), opting out of
	// systemd-confext's host ID/version matching entirely rather than
	// asserting a specific host ID to match against. The field is kept,
	// and callers may keep passing a value, purely so existing call
	// sites outside this package's own tests don't need to change; the
	// value itself is ignored by Build.
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
		Content: []byte(extensionReleaseContent),
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

// extensionReleaseContent is the confext extension-release file content
// written for every nanokube confext DDI, verbatim and unconditionally.
//
// ID=_any is a documented systemd convention: it tells systemd-confext
// this one extension declares no host affinity at all, so ID and
// VERSION_ID/SYSEXT_LEVEL matching are skipped entirely for it, without
// affecting matching for any other sysext/confext a real host might
// carry. Confirmed empirically against a real Fedora bootc host
// (systemd 259): `systemd-confext refresh --mutable=yes` (no --force)
// accepts an ID=_any extension outright ("Extension '...' matches '_any'
// OS."), where an extension-release carrying only ID=<host-id> (no
// version field) is instead rejected once the host's own /etc/os-release
// declares a VERSION_ID.
//
// This project deliberately chose ID=_any over the alternative of
// passing --force to `systemd-confext refresh` (tried and reverted; see
// agent/src/ops.rs's refresh doc history), for three reasons:
//
//  1. --force would have to apply at boot time too. The automatic
//     re-merge on every boot (systemd-confext.service) runs plain
//     `refresh`, with no flags, and is not something nanokube's agent
//     controls -- an --force-only fix would mean delivered config
//     silently vanishing on every reboot. ID=_any needs no such
//     accommodation: the opt-out is baked into the extension file
//     itself, so systemd-confext.service's own unmodified `refresh`
//     accepts it exactly the same way an agent-triggered one does.
//
//  2. Host os-release version matching is structurally at odds with
//     nanokube's update-ordering design: config is applied and
//     merge-verified BEFORE a reboot completes an image update, so at
//     refresh time the OLD image's os-release is still the live one --
//     matching against it does not verify compatibility with the NEW
//     (staged, not-yet-booted) image the config is meant for. The check
//     is checking the wrong thing at the wrong time for this
//     architecture's ordering.
//
//  3. nanokube already has stronger, purpose-built correctness
//     guarantees than systemd's generic ID/version matching: the
//     agent's own bookkeeping (expectedDigest <-> desiredName pairing),
//     render.Desired.Name()'s manifest-hash-based content identity, and
//     the single-writer invariant (only the agent ever writes to
//     /var/lib/confexts). systemd's ID/version check is redundant for
//     this architecture and actively conflicts with its update-ordering
//     model -- opting out via ID=_any is a deliberate, informed choice,
//     not a shortcut.
const extensionReleaseContent = "ID=_any\n"

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
