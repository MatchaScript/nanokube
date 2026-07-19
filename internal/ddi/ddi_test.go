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

// fedoraFileContexts is where selinux-policy-targeted installs the
// file_contexts database (same policy family as the node image — Step
// 2 design doc "SELinux ラベル規定", v0 procurement).
const fedoraFileContexts = "/etc/selinux/targeted/contexts/files/file_contexts"

// TestBuildAppliesSELinuxLabels verifies BuildInput.FileContextsPath
// actually reaches mkfs.erofs and produces real per-inode
// security.selinux xattrs instead of the Step 1 container_file_t
// regression (PR #27).
//
// The expected label per path is computed independently with
// matchpathcon against the SAME file_contexts database passed to
// Build, rather than a hardcoded type name: a build container's own
// selinux-policy-targeted is the sole source of truth (design doc
// "SELinux ラベル規定" — no hand-maintained label table), and which
// specific type a path maps to is not stable across Fedora policy
// versions. Confirmed empirically against the CI-pinned
// quay.io/fedora/fedora:42 image: its selinux-policy-targeted
// (42.24-1.fc42) defines no /etc/kubernetes-specific type at all and
// falls back to the generic etc_t, unlike some other systems' policy —
// so asserting a fixed type such as kubernetes_file_t here would be
// asserting something this policy version doesn't even claim.
func TestBuildAppliesSELinuxLabels(t *testing.T) {
	requireTools(t, "systemd-repart", "mkfs.erofs", "fsck.erofs", "matchpathcon")
	if _, err := os.Stat(fedoraFileContexts); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("CI requires selinux-policy-targeted in the ddi container: %v", err)
		}
		t.Skipf("no file_contexts at %s", fedoraFileContexts)
	}

	out := filepath.Join(t.TempDir(), "labeled.raw")
	err := Build(BuildInput{
		Name: "labeltest",
		Files: []render.File{
			{Path: "etc/kubernetes/manifests/etcd.yaml", Content: []byte("k: v\n"), Mode: 0o644},
			{Path: "etc/kubernetes/pki/ca.key", Content: []byte("KEY\n"), Mode: 0o600},
		},
		FileContextsPath: fedoraFileContexts,
	}, out)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, path := range []string{
		"/etc/kubernetes/manifests/etcd.yaml",
		"/etc/kubernetes/pki/ca.key",
	} {
		want := matchpathconContext(t, fedoraFileContexts, path)
		got := erofsXattr(t, out, path, "security.selinux")
		if !strings.Contains(got, want) {
			t.Errorf("%s: security.selinux = %q, want (via matchpathcon) %q", path, got, want)
		}
		if strings.Contains(got, "container_file_t") {
			t.Errorf("%s: container_file_t regression (Step 1 PR #27)", path)
		}
	}
}

// matchpathconContext independently computes the SELinux context
// fileContexts assigns path, using the same file_contexts database
// mkfs.erofs --file-contexts consumes. This is the oracle
// TestBuildAppliesSELinuxLabels checks Build's output against, instead
// of a hardcoded type name.
//
// -m f pins the lookup to regular-file entries, matching what
// mkfs.erofs itself queries for a regular file (erofs-utils'
// erofs_get_selabel_xattr passes the real source inode's mode into
// selabel_lookup). Without -m f, matchpathcon falls back to lstat'ing
// path on the TEST-RUNNING filesystem to infer a mode — and path here
// is a confext-tree path like "/etc/kubernetes/pki/ca.key" that does
// not exist on that filesystem, so the lstat fails and matchpathcon
// silently widens to an unfiltered (any file-kind) lookup. That
// widened lookup happens to agree with the file-kind-filtered one for
// the two paths this test checks today (both empirically confirmed
// against the CI-pinned Fedora 42 policy), but the two lookups are
// genuinely different queries against the same database (confirmed
// against libselinux's label_file.c) and can diverge for other paths
// — e.g. this exact database's /run and /mnt entries resolve
// differently unfiltered vs. -m f. Passing -m f makes this oracle
// query the same thing mkfs.erofs queries, on every axis, not just
// today's two paths.
func matchpathconContext(t *testing.T, fileContexts, path string) string {
	t.Helper()
	out, err := exec.Command("matchpathcon", "-n", "-m", "f", "-f", fileContexts, path).Output()
	if err != nil {
		t.Fatalf("matchpathcon %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

// erofsXattr reads the named xattr off pathInImage inside a built
// erofs image.
//
// dump.erofs --path reports only "Xattr size: N" for an inode, never
// the value (confirmed against erofs-utils 1.8.10 — no flag exposes
// it), so it can't be used here. Instead this extracts the image with
// fsck.erofs --extract=<dir> --xattrs, which asks the kernel to set
// each extracted file's security.selinux from the image's own stored
// value, then reads that xattr directly off the extracted file with
// syscall.Getxattr — one less CLI tool (getfattr) to depend on for a
// single read.
//
// The extraction's setxattr requires CAP_SYS_ADMIN (the kernel's
// generic security.* xattr-write fallback, cap_inode_setxattr, absent
// a more specific LSM hook) — the ddi CI job's container is granted
// --cap-add=SYS_ADMIN accordingly (ci.yaml) — and a destination
// filesystem that accepts security.selinux writes. On an
// SELinux-enabled podman host, the container's own overlay rootfs is
// not such a filesystem: writing security.selinux to it fails with
// ENOTSUP at every privilege level (rootless and rootful, --privileged
// included), while the same write to a bind-mounted host directory
// succeeds even rootless with SYS_ADMIN + label=disable. Verified
// 2026-07-19 on a Fedora SELinux-Enforcing host; full reproduction
// matrix in the workspace report docs/nanokube/reports/selinux.md.
// CI (ubuntu-24.04, no SELinux) writes to the container overlay
// without issue given the capability.
//
// So this test IS locally reproducible on an SELinux-Enforcing dev
// host: point TMPDIR at a bind-mounted host directory so t.TempDir()
// lands outside the overlay — recipe in README "Running the
// internal/ddi tests locally". (An earlier version of this comment
// declared local reproduction impossible, citing EPERM from this
// extraction step; the 2026-07-19 reproduction observed only ENOTSUP
// on the overlay path. The EPERM record plausibly came from runs
// without label=disable — an SELinux denial rather than the
// filesystem refusal — but that hypothesis is unverified.)
func erofsXattr(t *testing.T, image, pathInImage, name string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("fsck.erofs", "--offset="+erofsPartitionOffset, "--extract="+dir, "--xattrs", image)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fsck.erofs --extract %s: %v: %s", image, err, out)
	}

	extracted := filepath.Join(dir, pathInImage)
	size, err := syscall.Getxattr(extracted, name, nil)
	if err != nil {
		t.Fatalf("Getxattr(%s, %s) size: %v", extracted, name, err)
	}
	buf := make([]byte, size)
	if _, err := syscall.Getxattr(extracted, name, buf); err != nil {
		t.Fatalf("Getxattr(%s, %s): %v", extracted, name, err)
	}
	return string(buf)
}
