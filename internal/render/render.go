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

	corev1 "k8s.io/api/core/v1"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/controlplane"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/etcd"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubelet"
	staticpodutil "k8s.io/kubernetes/cmd/kubeadm/app/util/staticpod"

	"github.com/MatchaScript/nanokube/internal/certs"
	nanokubeadm "github.com/MatchaScript/nanokube/internal/kubeadm"
	"github.com/MatchaScript/nanokube/internal/layout"
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

// ManifestsPathPrefix is the confext-tree-relative directory the four
// control-plane static pod manifests are rendered under.
const ManifestsPathPrefix = "etc/kubernetes/manifests/"

// nodePKIDir is the path the rendered manifests must reference for
// PKI material: where certs land ON THE NODE once the confext tree is
// merged, independent of where ControlPlaneManifests actually
// generates them (a scratch directory).
const nodePKIDir = "/etc/kubernetes/pki"

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

// ambientCACertMountPaths mirrors kubeadm's own unexported
// controlplane.caCertsExtraVolumePaths (volumes.go): host paths that
// kubeadm's GetStaticPodSpecs conditionally mounts into apiserver/
// controller-manager if present ON THE RENDER HOST (an os.Stat check).
// That makes the manifest bytes depend on which of these happen to
// exist on whatever machine runs the render — ineligible for a
// content-hashed desired document (IMPLEMENTATION_PLAN §1.3). nanokube
// carries its own PKI end-to-end via the desired document (Task 4), so
// these distro CA-trust-store mounts serve no purpose here;
// stripAmbientHostMounts removes them unconditionally rather than
// conditionally reproducing render-host state. The always-present
// "/etc/ssl/certs" mount kubeadm also adds is NOT in this list: unlike
// these five, it is added unconditionally (not os.Stat-gated), so it
// doesn't vary by render host and is left as-is.
//
// Tradeoff, deliberate: this leaves apiserver/controller-manager with
// no path to system/public CA trust at all, only nanokube's own PKI.
// Nothing in nanokube's current scope needs it (no OIDC issuer,
// webhook backend, or cloud API signed by a public CA); a future
// feature that does need it must supply CA material via nanokube's own
// PKI/desired document, not rely on the host trust store.
var ambientCACertMountPaths = []string{
	"/etc/pki/ca-trust",
	"/etc/pki/tls/certs",
	"/etc/ca-certificates",
	"/usr/share/ca-certificates",
	"/usr/local/share/ca-certificates",
}

// ambientCACertVolumeName reproduces the exact volume-name mangling
// kubeadm's isExtraVolumeMountNeeded caller applies to each
// ambientCACertMountPaths entry (controlplane/volumes.go:
// strings.Replace(path, "/", "-", -1)[1:]). stripAmbientHostMounts
// matches on this generated Name in addition to the HostPath, so a
// hypothetical user-declared ExtraVolumes entry that happens to target
// one of these same paths under a name of its own choosing is never
// mistaken for kubeadm's ambient one (currently moot — nanokube sets
// no ExtraVolumes anywhere — but cheap to get right regardless).
func ambientCACertVolumeName(path string) string {
	return strings.Replace(path, "/", "-", -1)[1:]
}

// stripAmbientHostMounts removes any Volume (and matching
// VolumeMounts) that kubeadm generated for one of
// ambientCACertMountPaths — identified by both HostPath and Name, not
// HostPath alone.
func stripAmbientHostMounts(pod *corev1.Pod) {
	removedNames := map[string]bool{}
	var keptVolumes []corev1.Volume
	for _, v := range pod.Spec.Volumes {
		isAmbient := false
		if v.HostPath != nil {
			for _, p := range ambientCACertMountPaths {
				if v.HostPath.Path == p && v.Name == ambientCACertVolumeName(p) {
					isAmbient = true
					break
				}
			}
		}
		if isAmbient {
			removedNames[v.Name] = true
			continue
		}
		keptVolumes = append(keptVolumes, v)
	}
	pod.Spec.Volumes = keptVolumes

	for i := range pod.Spec.Containers {
		var keptMounts []corev1.VolumeMount
		for _, m := range pod.Spec.Containers[i].VolumeMounts {
			if removedNames[m.Name] {
				continue
			}
			keptMounts = append(keptMounts, m)
		}
		pod.Spec.Containers[i].VolumeMounts = keptMounts
	}
}

// ControlPlaneManifests renders the four control-plane static pod
// manifests (etcd, kube-apiserver, kube-controller-manager,
// kube-scheduler) against a throwaway scratch directory, then reads
// the results back. CertificatesDir on the cfg passed to these calls
// is pinned to nodePKIDir so the manifests reference where PKI lands
// on the node after the confext merge, not the scratch path used
// during rendering.
//
// The apiserver/controller-manager/scheduler specs come from
// controlplane.GetStaticPodSpecs directly (not the
// CreateInitStaticPodManifestFiles convenience wrapper, which always
// passes proxyEnvs=nil): passing an explicit, non-nil empty proxyEnvs
// pins that input instead of letting kubeadm scan the render process's
// os.Environ() for *_proxy variables and bake them in — the same
// "pin via explicit input" pattern used elsewhere (e.g.
// KubeletResolverConfig). stripAmbientHostMounts then removes the one
// remaining ambient behavior GetStaticPodSpecs has no input for. Both
// RootlessControlPlane and patchesDir handling from the wrapper are
// intentionally not reproduced: this repo never sets that feature gate
// (confirmed empty FeatureGates throughout) and always renders with no
// patches, so the wrapper's branches for them are unreachable dead
// weight here.
func ControlPlaneManifests(cfg *kubeadmapi.InitConfiguration) ([]File, error) {
	scratch, err := os.MkdirTemp("", "nanokube-render-manifests-*")
	if err != nil {
		return nil, fmt.Errorf("render: scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	own := *cfg
	own.ClusterConfiguration.CertificatesDir = nodePKIDir

	const isDryRun = false
	if err := etcd.CreateLocalEtcdStaticPodManifestFile(
		scratch, "", own.NodeRegistration.Name, &own.ClusterConfiguration, &own.LocalAPIEndpoint, isDryRun,
	); err != nil {
		return nil, fmt.Errorf("render: etcd manifest: %w", err)
	}

	specs := controlplane.GetStaticPodSpecs(&own.ClusterConfiguration, &own.LocalAPIEndpoint, []kubeadmapi.EnvVar{})
	for name, spec := range specs {
		stripAmbientHostMounts(&spec)
		if err := staticpodutil.WriteStaticPodToDisk(name, scratch, spec); err != nil {
			return nil, fmt.Errorf("render: write %s manifest: %w", name, err)
		}
	}

	names := []string{"etcd", "kube-apiserver", "kube-controller-manager", "kube-scheduler"}
	files := make([]File, 0, len(names))
	for _, n := range names {
		content, err := os.ReadFile(filepath.Join(scratch, n+".yaml"))
		if err != nil {
			return nil, fmt.Errorf("render: read back %s: %w", n, err)
		}
		files = append(files, File{Path: ManifestsPathPrefix + n + ".yaml", Content: content, Mode: 0o644})
	}
	return files, nil
}

// kubeconfigNames are the node-delivered kubeconfigs. super-admin.conf
// is deliberately absent: it is a client-side credential of init/CLI
// with a create-use-delete lifecycle that the confext distribution
// model cannot express (Step 2 design doc "スコープ").
var kubeconfigNames = []string{"admin.conf", "controller-manager.conf", "scheduler.conf", "kubelet.conf"}

// Credentials materializes the full PKI + kubeconfig set into credsDir
// (the render side's persistent secret store — NOT a distribution
// path) via certs.EnsureAll, then copies it into confext-tree Files.
// EnsureAll preserves existing valid files, so rendering twice against
// the same credsDir is byte-identical and the revision is stable.
//
// Not safe to call concurrently against the same credsDir: EnsureAll's
// underlying kubeadm writers are check-then-write with no locking, so
// two concurrent first-renders of a fresh credsDir can race and leave
// a torn PKI (e.g. a CA cert paired with a different run's key). Every
// current caller renders sequentially; a future concurrent caller
// (e.g. an operator reconciler with MaxConcurrentReconciles > 1) must
// serialize on credsDir itself.
func Credentials(cfg *kubeadmapi.InitConfiguration, credsDir string) ([]File, error) {
	l := layout.Rooted(credsDir)
	if err := certs.NewSigner(cfg, l).EnsureAll(); err != nil {
		return nil, fmt.Errorf("render: ensure credentials: %w", err)
	}

	var files []File
	err := filepath.WalkDir(l.PKIDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(l.PKIDir, p)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if strings.HasSuffix(p, ".key") {
			mode = 0o600
		}
		files = append(files, File{Path: "etc/kubernetes/pki/" + filepath.ToSlash(rel), Content: content, Mode: mode})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("render: walk pki: %w", err)
	}
	for _, name := range kubeconfigNames {
		content, err := os.ReadFile(filepath.Join(l.KubernetesDir, name))
		if err != nil {
			return nil, fmt.Errorf("render: read kubeconfig %s: %w", name, err)
		}
		files = append(files, File{Path: "etc/kubernetes/" + name, Content: content, Mode: 0o600})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// MaxRenderedBytes caps the total rendered file set (sum of every
// File.Content length). Renders land in etcd-backed objects on the
// operator side, and unbounded user content (files:, extra args) must
// fail at render time on the writer's side, not wedge every subsequent
// write (an OCPBUGS-62619-shaped failure). The agent's receive
// guardrail is a separate, larger limit and is unrelated to this cap.
const MaxRenderedBytes = 512 << 10

// validateSize rejects a Desired whose total File.Content bytes would
// exceed MaxRenderedBytes.
func (d Desired) validateSize() error {
	total := 0
	for _, f := range d.Files {
		total += len(f.Content)
	}
	if total >= MaxRenderedBytes {
		return fmt.Errorf("render: rendered set is %d bytes, cap is %d", total, MaxRenderedBytes)
	}
	return nil
}

// ControlPlaneDesired assembles the complete control-plane confext:
// kubelet config + flags env, the four static pod manifests, and the
// full PKI + kubeconfig set. This (and WorkerDesired) is the only
// place a control-plane Desired may be assembled — the desired
// document is one blob per node, so pushing a Desired built from a
// subset of these classes would silently remove the missing ones from
// the node on the next confext refresh.
func ControlPlaneDesired(cfg *kubeadmapi.InitConfiguration, credsDir string) (Desired, error) {
	kubeletCfg, err := KubeletConfig(cfg)
	if err != nil {
		return Desired{}, err
	}
	manifests, err := ControlPlaneManifests(cfg)
	if err != nil {
		return Desired{}, err
	}
	creds, err := Credentials(cfg, credsDir)
	if err != nil {
		return Desired{}, err
	}
	files := []File{kubeletCfg, KubeletFlagsEnv(cfg)}
	files = append(files, manifests...)
	files = append(files, creds...)
	d := Desired{Files: files}
	if err := d.validateSize(); err != nil {
		return Desired{}, err
	}
	return d, nil
}

// WorkerDesired assembles the worker confext: kubelet config + flags
// env, plus the TLS-bootstrap kubeconfig when joining
// (bootstrapKubeconfig non-nil). See ControlPlaneDesired's doc comment
// for why this is the only place a worker Desired may be assembled.
//
// The bootstrap kubeconfig stays in the confext after the join
// completes; it is inert once /etc/kubernetes/kubelet.conf exists
// (kubelet ignores --bootstrap-kubeconfig then) and its token has
// expired.
func WorkerDesired(cfg *kubeadmapi.InitConfiguration, bootstrapKubeconfig []byte) (Desired, error) {
	kubeletCfg, err := KubeletConfig(cfg)
	if err != nil {
		return Desired{}, err
	}
	files := []File{kubeletCfg, KubeletFlagsEnv(cfg)}
	if bootstrapKubeconfig != nil {
		files = append(files, File{Path: "etc/kubernetes/bootstrap-kubelet.conf", Content: bootstrapKubeconfig, Mode: 0o600})
	}
	d := Desired{Files: files}
	if err := d.validateSize(); err != nil {
		return Desired{}, err
	}
	return d, nil
}
