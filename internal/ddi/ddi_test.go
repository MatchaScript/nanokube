package ddi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatchaScript/nanokube/internal/render"
)

// testInput returns a small, deterministic BuildInput good enough to
// exercise a real systemd-repart build: one config file plus the
// extension-release Build synthesizes.
func testInput() BuildInput {
	return BuildInput{
		Name:               "testconfext",
		ExtensionReleaseID: "fedora",
		Files: []render.File{
			{Path: "etc/kubernetes/kubelet-config.yaml", Content: []byte("kind: KubeletConfiguration\n")},
		},
	}
}

// skipIfRepartUnusable reports whether err indicates an environment
// that can't complete a real DDI build here — either systemd-repart is
// missing entirely, or it's present but the erofs backend it shells out
// to (mkfs.erofs, from erofs-utils) isn't installed. Both are expected
// on a bare host; the fix is a Fedora container with systemd-container
// + systemd-udev + erofs-utils, not modifying this host.
func skipIfRepartUnusable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if errors.Is(err, ErrSystemdRepartNotFound) {
		t.Skipf("systemd-repart not found in PATH: skipping real DDI build (needs systemd-repart)")
	}
	if strings.Contains(err.Error(), "mkfs.erofs") {
		t.Skipf("systemd-repart present but mkfs.erofs unavailable: skipping real DDI build (a Fedora container with systemd-container+systemd-udev+erofs-utils installed provides it): %v", err)
	}
	t.Fatalf("Build: %v", err)
}

func TestBuild_UnsignedProducesRawImage(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "testconfext.raw")

	err := Build(testInput(), out)
	skipIfRepartUnusable(t, err)

	info, statErr := os.Stat(out)
	if statErr != nil {
		t.Fatalf("stat output: %v", statErr)
	}
	// DDIs carry real partition-table overhead even for a couple of
	// small files; anything under 1KB would mean repart silently wrote
	// something bogus rather than a disk image.
	const minPlausibleSize = 1024
	if info.Size() < minPlausibleSize {
		t.Fatalf("output %s is %d bytes, want > %d (suspiciously tiny for a DDI)", out, info.Size(), minPlausibleSize)
	}
}

func TestBuild_SameInputTwiceBothSucceed(t *testing.T) {
	dir := t.TempDir()
	input := testInput()

	out1 := filepath.Join(dir, "a.raw")
	err1 := Build(input, out1)
	skipIfRepartUnusable(t, err1)

	out2 := filepath.Join(dir, "b.raw")
	err2 := Build(input, out2)
	skipIfRepartUnusable(t, err2)

	// systemd-repart does not guarantee byte-reproducible output
	// without --seed, so we only assert both builds succeed and
	// produce a real file — not that they're byte-identical.
	for _, p := range []string{out1, out2} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", p)
		}
	}
}

func TestBuild_SystemdRepartNotFound(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)

	err := Build(testInput(), filepath.Join(t.TempDir(), "out.raw"))
	if !errors.Is(err, ErrSystemdRepartNotFound) {
		t.Fatalf("Build with empty PATH: got err %v, want ErrSystemdRepartNotFound", err)
	}
}

// TestBuild_SignedNeedsBothKeyAndCert checks that a partially-set
// signing config (only PrivateKeyPath, no CertificatePath) does not
// engage signing: Build must still take the unsigned
// --exclude-partitions=root-verity-sig path and succeed, rather than
// e.g. passing --private-key alone to systemd-repart and failing.
func TestBuild_SignedNeedsBothKeyAndCert(t *testing.T) {
	dir := t.TempDir()
	input := testInput()
	input.PrivateKeyPath = filepath.Join(dir, "does-not-exist.key")
	// CertificatePath intentionally left empty: signing must not engage.

	out := filepath.Join(dir, "out.raw")
	err := Build(input, out)
	// With signing not engaged, this should behave exactly like the
	// unsigned path: either skip (repart/erofs unavailable) or succeed.
	skipIfRepartUnusable(t, err)

	if _, statErr := os.Stat(out); statErr != nil {
		t.Fatalf("stat output: %v", statErr)
	}
}

// TestBuild_ExtensionReleaseConvention checks the extension-release
// marker file's path and content convention in isolation (no
// systemd-repart involved): it must live at
// etc/extension-release.d/extension-release.<name> — nested under
// etc/, not usr/lib/ (that's sysext's convention, not confext's) —
// since confext's built-in CopyFiles=/etc/ rule only pulls from
// <copy-source>/etc/.
func TestBuild_ExtensionReleaseConvention(t *testing.T) {
	tree := t.TempDir()
	release := render.File{
		Path:    filepath.Join(extensionReleaseDir, fmt.Sprintf("extension-release.%s", "testconfext")),
		Content: []byte("ID=fedora\n"),
	}
	if err := writeTreeFile(tree, release); err != nil {
		t.Fatalf("writeTreeFile: %v", err)
	}

	wantPath := filepath.Join(tree, "etc", "extension-release.d", "extension-release.testconfext")
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read written extension-release at %s: %v", wantPath, err)
	}
	if string(got) != "ID=fedora\n" {
		t.Fatalf("extension-release content = %q, want %q", got, "ID=fedora\n")
	}
}
