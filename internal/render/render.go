// Package render produces the per-node desired document: a fully
// rendered, content-hash-named bundle of confext-layout files.
// Rendering happens once, here, using kubeadm's own library calls —
// nodes never template or default-fill on their own.
package render

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubelet"

	nanokubeadm "github.com/MatchaScript/nanokube/internal/kubeadm"
)

// KubeletConfigPath is the confext-tree-relative location of the
// rendered kubelet configuration. Which path the kubelet process
// actually reads is decided by the image's kubelet drop-in (--config);
// this package only owns the rendering, not the drop-in.
const KubeletConfigPath = "etc/kubernetes/kubelet-config.yaml"

// KubeletFlagsEnvPath is the confext-tree-relative path of the kubelet
// dynamic env file. Step 2 moves it from /var/lib/kubelet (outside the
// confext's /etc reach) to /etc/kubernetes; the kubelet drop-in's
// EnvironmentFile= follows (devenv image overlay).
const KubeletFlagsEnvPath = "etc/kubernetes/kubeadm-flags.env"

// File is one entry in a Desired document's file list. Path is
// relative to the confext tree root, e.g.
// "etc/kubernetes/kubelet-config.yaml".
type File struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// Desired is the per-node desired document: a fully rendered file
// list. It holds no apply-mode — reboot/staging decisions are made
// elsewhere.
type Desired struct {
	Files []File
}

// Name returns a deterministic identifier derived from d's rendered
// files only: sha256 over every (Path, Mode, Content) pair, sorted by
// Path so field order never matters, hex-encoded. Equal input always
// yields the same Name; changing any file's path or content always
// yields a different one. The result is lowercase alnum only, a valid
// systemd extension/confext version name, and doubles as the
// bookkeeping key for later stages — including the trigger for
// rebuilding the confext DDI itself.
//
// Name() = revision (see IMPLEMENTATION_PLAN.md §2).
func (d Desired) Name() string {
	h := sha256.New()

	files := append([]File(nil), d.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		writeChunk(h, []byte(f.Path))
		var mode [4]byte
		binary.BigEndian.PutUint32(mode[:], uint32(f.Mode))
		writeChunk(h, mode[:])
		writeChunk(h, f.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeChunk feeds a length-prefixed b into h so that the boundary
// between consecutive chunks (e.g. a Path and its Content, or one
// file's pair and the next's) can never be ambiguous.
func writeChunk(h io.Writer, b []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b)))
	h.Write(length[:])
	h.Write(b)
}

// KubeletConfig renders the kubelet KubeletConfiguration carried by
// cfg to bytes at KubeletConfigPath. It reuses kubeadm's own
// WriteInstanceConfigToDisk + WriteConfigToDisk calls — the same pair
// internal/kubeadm's ensureKubeletFiles makes, in the same order —
// against a throwaway scratch directory, then reads the result back.
// That gives byte-for-byte parity with what the existing on-disk path
// produces, without reimplementing kubeadm's marshalling or its
// NodeLocalCRISocket instance-config patch logic.
func KubeletConfig(cfg *kubeadmapi.InitConfiguration) (File, error) {
	scratch, err := os.MkdirTemp("", "nanokube-render-kubelet-*")
	if err != nil {
		return File{}, fmt.Errorf("render: scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	instance := &kubeletconfigv1beta1.KubeletConfiguration{
		ContainerRuntimeEndpoint: cfg.NodeRegistration.CRISocket,
	}
	if err := kubelet.WriteInstanceConfigToDisk(instance, scratch); err != nil {
		return File{}, fmt.Errorf("render: kubelet instance config: %w", err)
	}

	// WriteInstanceConfigToDisk (above) only ever writes
	// containerRuntimeEndpoint to instance-config.yaml — it silently
	// drops every other field on the KubeletConfiguration passed to
	// it, so ResolverConfig cannot be pinned there. Instead pin it on
	// the ClusterConfiguration's kubelet component config, which
	// WriteConfigToDisk (below) does marshal. The pin is shared with
	// internal/kubeadm's ensureKubeletFiles so this render path and
	// the transitional on-disk path stay byte-identical on any host.
	if err := nanokubeadm.PinKubeletResolverConfig(&cfg.ClusterConfiguration); err != nil {
		return File{}, fmt.Errorf("render: %w", err)
	}

	const patchesDir = ""
	if err := kubelet.WriteConfigToDisk(&cfg.ClusterConfiguration, scratch, patchesDir, io.Discard); err != nil {
		return File{}, fmt.Errorf("render: kubelet config: %w", err)
	}

	content, err := os.ReadFile(filepath.Join(scratch, kubeadmconstants.KubeletConfigurationFileName))
	if err != nil {
		return File{}, fmt.Errorf("render: read rendered kubelet config: %w", err)
	}
	return File{Path: KubeletConfigPath, Content: content, Mode: 0o644}, nil
}

// KubeletFlagsEnv renders kubeadm-flags.env without kubeadm's
// WriteKubeletDynamicEnvFile: that writer decides whether to emit
// --hostname-override by comparing against the render host's hostname
// (GetHostname("")), which is meaningless off-node and makes the bytes
// depend on where rendering runs. Node identity is explicit input here,
// so the override is emitted unconditionally; KubeletExtraArgs pass
// through in order. Format parity is pinned by the
// ReadKubeletDynamicEnvFile round-trip test.
func KubeletFlagsEnv(cfg *kubeadmapi.InitConfiguration) File {
	args := []string{"--hostname-override=" + cfg.NodeRegistration.Name}
	for _, a := range cfg.NodeRegistration.KubeletExtraArgs {
		args = append(args, "--"+a.Name+"="+a.Value)
	}
	content := fmt.Sprintf("%s=%q\n", "KUBELET_KUBEADM_ARGS", strings.Join(args, " "))
	return File{Path: KubeletFlagsEnvPath, Content: []byte(content), Mode: 0o644}
}
