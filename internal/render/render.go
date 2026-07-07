// Package render produces the per-node desired document: a fully
// rendered, content-hash-named bundle of confext-layout files plus the
// target bootc image digest. Rendering happens once, here, using
// kubeadm's own library calls — nodes never template or default-fill
// on their own.
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

	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/componentconfigs"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubelet"
	"k8s.io/utils/ptr"
)

// KubeletConfigPath is the confext-tree-relative location of the
// rendered kubelet configuration. Which path the kubelet process
// actually reads is decided by the image's kubelet drop-in (--config);
// this package only owns the rendering, not the drop-in.
const KubeletConfigPath = "etc/kubernetes/kubelet-config.yaml"

// kubeletResolverConfig is pinned explicitly so the rendered bytes never
// depend on the render host: kubeadm's own defaulting probes whether
// systemd-resolved is active on the machine doing the rendering, which
// is a Kind container here, not the node. Nodes run systemd-resolved
// (homelab bootc images), so the resolved stub path is the correct
// value for every node.
const kubeletResolverConfig = "/run/systemd/resolve/resolv.conf"

// File is one entry in a Desired document's file list. Path is
// relative to the confext tree root, e.g.
// "etc/kubernetes/kubelet-config.yaml".
type File struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// Desired is the per-node desired document: a fully rendered file list
// and the target image digest, applied together as one atom. It holds
// no apply-mode — reboot/staging decisions are made elsewhere.
type Desired struct {
	ImageDigest string
	Files       []File
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
// Name is intentionally insensitive to ImageDigest: a pure OS image
// update (digest changes, rendered config files don't) must not force
// a pointless DDI rebuild, so the digest is excluded from the hash
// even though it remains part of the Desired document and is applied
// atomically alongside the files.
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
	// it, so setting ResolverConfig there does not propagate to the
	// rendered output. Instead, pin it on the ClusterConfiguration's
	// kubelet component config, which WriteConfigToDisk (below) does
	// marshal: kubeadm's own componentconfigs.kubeletConfig.Mutate()
	// only fills ResolverConfig from render-host systemd-resolved
	// detection when it is nil, so setting it here first makes that
	// detection a no-op.
	kubeletCfg, ok := cfg.ClusterConfiguration.ComponentConfigs[componentconfigs.KubeletGroup]
	if !ok {
		return File{}, fmt.Errorf("render: no kubelet component config found in ClusterConfiguration")
	}
	kubeletConfiguration, ok := kubeletCfg.Get().(*kubeletconfigv1beta1.KubeletConfiguration)
	if !ok {
		return File{}, fmt.Errorf("render: unexpected kubelet component config type %T", kubeletCfg.Get())
	}
	kubeletConfiguration.ResolverConfig = ptr.To(kubeletResolverConfig)
	kubeletCfg.Set(kubeletConfiguration)

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
