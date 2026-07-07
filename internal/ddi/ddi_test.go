package ddi

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/MatchaScript/nanokube/internal/render"
)

// testInput returns a small, deterministic BuildInput good enough to
// exercise a real systemd-repart build: one config file plus the
// extension-release Build synthesizes.
func testInput() BuildInput {
	return BuildInput{
		Name: "testconfext",
		Files: []render.File{
			{Path: "etc/kubernetes/kubelet-config.yaml", Content: []byte("kind: KubeletConfiguration\n")},
		},
	}
}

// requireTools skips locally when a build/inspect tool is missing, but
// fails in CI: a silently skipped verification is how the mtree check
// vanished from Step 1 (rev6 "DDI ビルドの規定").
func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s is required in CI but not found: %v", tool, err)
			}
			t.Skipf("%s not in PATH", tool)
		}
	}
}

// erofsPartitionOffset is where --make-ddi=confext places the root
// (erofs) partition: GPT sector 40 * 512 bytes. Verified with sfdisk -J
// against an unsigned build (systemd 259).
const erofsPartitionOffset = "20480"

func TestBuildStripsAmbientOwnershipAndMode(t *testing.T) {
	requireTools(t, "systemd-repart", "mkfs.erofs", "dump.erofs")

	// A deliberately hostile umask: without explicit chmod the 0o644
	// file below would land as 0o600.
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	out := filepath.Join(t.TempDir(), "test.raw")
	err := Build(BuildInput{
		Name: "ownertest",
		Files: []render.File{
			{Path: "etc/test.conf", Content: []byte("k=v\n"), Mode: 0o644},
			{Path: "etc/secret.conf", Content: []byte("s=1\n"), Mode: 0o600},
		},
	}, out)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for path, want := range map[string][]string{
		"/etc/test.conf":   {"Uid: 0", "Gid: 0", "0644/rw-r--r--"},
		"/etc/secret.conf": {"Uid: 0", "Gid: 0", "0600/rw-------"},
	} {
		got := dumpErofs(t, out, path)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: dump.erofs output missing %q:\n%s", path, w, got)
			}
		}
	}
}

func dumpErofs(t *testing.T, image, path string) string {
	t.Helper()
	cmd := exec.Command("dump.erofs", "--offset="+erofsPartitionOffset, "--path="+path, image)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dump.erofs %s: %v: %s", path, err, out)
	}
	return string(out)
}

func TestBuildRejectsHalfConfiguredSigning(t *testing.T) {
	out := filepath.Join(t.TempDir(), "x.raw")
	for _, in := range []BuildInput{
		{Name: "half", PrivateKeyPath: "/nonexistent/key.pem"},
		{Name: "half", CertificatePath: "/nonexistent/cert.crt"},
	} {
		err := Build(in, out)
		if err == nil {
			t.Errorf("Build with half-configured signing (key=%q cert=%q): want error, got nil",
				in.PrivateKeyPath, in.CertificatePath)
			continue
		}
		if errors.Is(err, ErrSystemdRepartNotFound) {
			t.Errorf("Build with half-configured signing (key=%q cert=%q): want input-validation error, got ErrSystemdRepartNotFound (validation must run before the tool-lookup, so this test isn't vacuous on tool-less hosts): %v",
				in.PrivateKeyPath, in.CertificatePath, err)
		}
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
		Content: []byte(extensionReleaseContent),
	}
	if err := writeTreeFile(tree, release); err != nil {
		t.Fatalf("writeTreeFile: %v", err)
	}

	wantPath := filepath.Join(tree, "etc", "extension-release.d", "extension-release.testconfext")
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read written extension-release at %s: %v", wantPath, err)
	}
	if string(got) != "ID=_any\n" {
		t.Fatalf("extension-release content = %q, want %q", got, "ID=_any\n")
	}
}

// TestExtensionReleaseContent_IsAlwaysAny locks in the ID=_any fix (see
// extensionReleaseContent's doc comment for the full rationale): every
// confext's extension-release file must declare ID=_any, since opting out
// of systemd-confext's host ID/version matching entirely is what makes a
// genuine, unmodified `systemd-confext refresh` (no --force) accept the
// merge on a real host.
func TestExtensionReleaseContent_IsAlwaysAny(t *testing.T) {
	if extensionReleaseContent != "ID=_any\n" {
		t.Fatalf("extensionReleaseContent = %q, want %q", extensionReleaseContent, "ID=_any\n")
	}
}
