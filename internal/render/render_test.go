package render

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubelet"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	"github.com/MatchaScript/nanokube/internal/kubeadm"
	"github.com/MatchaScript/nanokube/internal/layouttest"
)

func TestCredentials_RendersPKIAndKubeconfigs(t *testing.T) {
	cfg := defaultedInit(t)
	creds := t.TempDir()
	files, err := Credentials(cfg, creds)
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	byPath := map[string]File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	for _, p := range []string{
		"etc/kubernetes/pki/ca.crt",
		"etc/kubernetes/pki/ca.key",
		"etc/kubernetes/pki/etcd/ca.crt",
		"etc/kubernetes/admin.conf",
		"etc/kubernetes/controller-manager.conf",
		"etc/kubernetes/scheduler.conf",
		"etc/kubernetes/kubelet.conf",
	} {
		if _, ok := byPath[p]; !ok {
			t.Errorf("missing %s", p)
		}
	}
	if _, ok := byPath["etc/kubernetes/super-admin.conf"]; ok {
		t.Error("super-admin.conf must NOT be in the confext (client-side credential)")
	}
	if m := byPath["etc/kubernetes/pki/ca.key"].Mode; m != 0o600 {
		t.Errorf("ca.key Mode = %o, want 0600", m)
	}
	if m := byPath["etc/kubernetes/admin.conf"].Mode; m != 0o600 {
		t.Errorf("admin.conf Mode = %o, want 0600", m)
	}
	if m := byPath["etc/kubernetes/pki/ca.crt"].Mode; m != 0o644 {
		t.Errorf("ca.crt Mode = %o, want 0644", m)
	}
}

func TestCredentials_StableAcrossRenders(t *testing.T) {
	cfg := defaultedInit(t)
	creds := t.TempDir()
	a, err := Credentials(cfg, creds)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Credentials(cfg, creds) // same persistent creds dir
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Error("re-render with the same credentials dir must be byte-identical (EnsureAll idempotency)")
	}
}

// defaultedInit builds a defaulted control-plane InitConfiguration, the
// same fixture internal/boot's tests use (see boot_test.go's
// defaultedLoaded), but returns the *kubeadmapi.InitConfiguration
// directly since this package has no config.Loaded wrapper to build.
func defaultedInit(t *testing.T) *kubeadmapi.InitConfiguration {
	t.Helper()
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("DefaultedStaticInitConfiguration: %v", err)
	}
	return cfg
}

func TestKubeletConfig_Determinism(t *testing.T) {
	cfg := defaultedInit(t)

	f1, err := KubeletConfig(cfg)
	if err != nil {
		t.Fatalf("KubeletConfig (1st call): %v", err)
	}
	f2, err := KubeletConfig(cfg)
	if err != nil {
		t.Fatalf("KubeletConfig (2nd call): %v", err)
	}

	if !bytes.Equal(f1.Content, f2.Content) {
		t.Fatalf("KubeletConfig output differs across repeated calls:\n1: %s\n2: %s", f1.Content, f2.Content)
	}

	d1 := Desired{Files: []File{f1}}
	d2 := Desired{Files: []File{f2}}
	if d1.Name() != d2.Name() {
		t.Fatalf("Desired.Name() not deterministic: %q vs %q", d1.Name(), d2.Name())
	}
}

func TestDesired_Name_SensitiveToFileContent(t *testing.T) {
	a := Desired{
		Files: []File{{Path: "etc/kubernetes/kubelet-config.yaml", Content: []byte("content-a")}},
	}
	b := Desired{
		Files: []File{{Path: "etc/kubernetes/kubelet-config.yaml", Content: []byte("content-b")}},
	}

	if a.Name() == b.Name() {
		t.Fatalf("Name() unchanged after file content change: both %q", a.Name())
	}
}

func TestDesired_Name_IsValidConfextName(t *testing.T) {
	d := Desired{
		Files: []File{{Path: "etc/kubernetes/kubelet-config.yaml", Content: []byte("hello")}},
	}
	name := d.Name()

	valid := regexp.MustCompile(`^[a-z0-9]+$`)
	if !valid.MatchString(name) {
		t.Fatalf("Name() = %q, want lowercase alnum only", name)
	}
}

// TestKubeletConfig_ParityWithEnsureWorker proves render.KubeletConfig
// produces byte-identical content to the existing on-disk path
// (internal/kubeadm.EnsureWorker, which calls the same
// WriteInstanceConfigToDisk + WriteConfigToDisk pair) for the same
// input configuration — not a hand-copied golden string.
func TestKubeletConfig_ParityWithEnsureWorker(t *testing.T) {
	cfg := defaultedInit(t)
	l := layouttest.New(t)

	if err := kubeadm.EnsureWorker(cfg, l); err != nil {
		t.Fatalf("EnsureWorker: %v", err)
	}
	want, err := os.ReadFile(l.KubeletConfigFile)
	if err != nil {
		t.Fatalf("read EnsureWorker output: %v", err)
	}

	got, err := KubeletConfig(cfg)
	if err != nil {
		t.Fatalf("KubeletConfig: %v", err)
	}

	if got.Path != KubeletConfigPath {
		t.Errorf("File.Path = %q, want %q", got.Path, KubeletConfigPath)
	}
	if !bytes.Equal(got.Content, want) {
		t.Errorf("KubeletConfig content differs from EnsureWorker's on-disk output:\ngot:  %s\nwant: %s", got.Content, want)
	}
}

// TestKubeletConfigResolvConfIsExplicit guards against kubeadm's own
// kubelet-config defaulting, which probes whether systemd-resolved is
// active on the render host (a Kind container, not the node) and
// bakes that answer into resolvConf. Nodes always run systemd-resolved
// (homelab bootc images), so the value must be pinned explicitly and
// must not depend on render-host state.
func TestKubeletConfigResolvConfIsExplicit(t *testing.T) {
	cfg := defaultedInit(t)
	f, err := KubeletConfig(cfg)
	if err != nil {
		t.Fatalf("KubeletConfig: %v", err)
	}
	want := "resolvConf: /run/systemd/resolve/resolv.conf"
	if !strings.Contains(string(f.Content), want) {
		t.Errorf("rendered kubelet config must pin %q explicitly (render-host state must not leak); got:\n%s",
			want, f.Content)
	}
}

func TestNameChangesWhenOnlyModeChanges(t *testing.T) {
	a := Desired{Files: []File{{Path: "etc/x", Content: []byte("c"), Mode: 0o644}}}
	b := Desired{Files: []File{{Path: "etc/x", Content: []byte("c"), Mode: 0o600}}}
	if a.Name() == b.Name() {
		t.Error("Name() must change when only a file's Mode changes")
	}
}

// TestKubeletFlagsEnv_HostnameOverrideIsUnconditional guards against
// reintroducing kubeadm's WriteKubeletDynamicEnvFile behavior, which
// only emits --hostname-override when the configured node name differs
// from the render host's own hostname (GetHostname("")) — an ambient
// comparison that has no meaning off-node. KubeletFlagsEnv must emit
// the override unconditionally, regardless of what machine renders it.
func TestKubeletFlagsEnv_HostnameOverrideIsUnconditional(t *testing.T) {
	cfg := defaultedInit(t)
	cfg.NodeRegistration.Name = "test-node-0"

	f := KubeletFlagsEnv(cfg)
	if f.Path != KubeletFlagsEnvPath {
		t.Errorf("Path = %q, want %q", f.Path, KubeletFlagsEnvPath)
	}
	if f.Mode != 0o644 {
		t.Errorf("Mode = %o, want 0644", f.Mode)
	}
	want := `KUBELET_KUBEADM_ARGS="--hostname-override=test-node-0"` + "\n"
	if string(f.Content) != want {
		t.Errorf("Content = %q, want %q (must not depend on the render host's hostname)", f.Content, want)
	}
}

// TestKubeletFlagsEnv_ParsesWithKubeadmReader proves our hand-rendered
// file is a format kubeadm's own ReadKubeletDynamicEnvFile can parse,
// so switching writers doesn't change what the kubelet drop-in reads.
func TestKubeletFlagsEnv_ParsesWithKubeadmReader(t *testing.T) {
	cfg := defaultedInit(t)
	cfg.NodeRegistration.Name = "test-node-0"

	f := KubeletFlagsEnv(cfg)
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeadm-flags.env")
	if err := os.WriteFile(path, f.Content, 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := kubelet.ReadKubeletDynamicEnvFile(path)
	if err != nil {
		t.Fatalf("kubeadm's own reader must parse our env file: %v", err)
	}
	if len(args) != 1 || args[0] != "--hostname-override=test-node-0" {
		t.Errorf("parsed args = %v", args)
	}
}

func TestKubeletFlagsEnv_ExtraArgsPassThrough(t *testing.T) {
	cfg := defaultedInit(t)
	cfg.NodeRegistration.Name = "test-node-0"
	cfg.NodeRegistration.KubeletExtraArgs = []kubeadmapi.Arg{{Name: "node-ip", Value: "10.0.0.5"}}

	f := KubeletFlagsEnv(cfg)
	want := `KUBELET_KUBEADM_ARGS="--hostname-override=test-node-0 --node-ip=10.0.0.5"` + "\n"
	if string(f.Content) != want {
		t.Errorf("Content = %q, want %q", f.Content, want)
	}
}

func TestControlPlaneManifests_RendersFourStaticPods(t *testing.T) {
	cfg := defaultedInit(t)
	cfg.LocalAPIEndpoint.AdvertiseAddress = "192.0.2.10"

	files, err := ControlPlaneManifests(cfg)
	if err != nil {
		t.Fatalf("ControlPlaneManifests: %v", err)
	}
	wantPaths := []string{
		"etc/kubernetes/manifests/etcd.yaml",
		"etc/kubernetes/manifests/kube-apiserver.yaml",
		"etc/kubernetes/manifests/kube-controller-manager.yaml",
		"etc/kubernetes/manifests/kube-scheduler.yaml",
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
		if f.Mode != 0o644 {
			t.Errorf("%s: Mode = %o, want 0644", f.Path, f.Mode)
		}
	}
	if !slices.Equal(got, wantPaths) {
		t.Errorf("paths = %v, want %v", got, wantPaths)
	}
	// Manifests must reference the NODE's pki path, not the scratch dir.
	for _, f := range files {
		if bytes.Contains(f.Content, []byte(os.TempDir())) {
			t.Errorf("%s leaks scratch path", f.Path)
		}
	}
	apiserver := files[1]
	if !bytes.Contains(apiserver.Content, []byte("advertise-address=192.0.2.10")) {
		t.Errorf("apiserver manifest missing advertise address from cfg")
	}
	if !bytes.Contains(apiserver.Content, []byte("/etc/kubernetes/pki")) {
		t.Errorf("apiserver manifest must mount /etc/kubernetes/pki")
	}
}

func TestControlPlaneManifests_Deterministic(t *testing.T) {
	cfg := defaultedInit(t)
	cfg.LocalAPIEndpoint.AdvertiseAddress = "192.0.2.10"

	a, err := ControlPlaneManifests(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ControlPlaneManifests(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Error("two renders of the same cfg differ (ambient leak?)")
	}
}

// TestControlPlaneManifests_IgnoresRenderHostProxyEnv is a sentinel-swap
// check for the ambient read this task pins away: kubeadm's
// GetStaticPodSpecs, called with proxyEnvs=nil (as
// CreateInitStaticPodManifestFiles always does), scans the render
// process's os.Environ() for *_proxy variables and bakes them into the
// apiserver/controller-manager/scheduler container Env. Flipping the
// sentinel (setting HTTP_PROXY) must not change the rendered bytes.
func TestControlPlaneManifests_IgnoresRenderHostProxyEnv(t *testing.T) {
	cfg := defaultedInit(t)
	cfg.LocalAPIEndpoint.AdvertiseAddress = "192.0.2.10"

	before, err := ControlPlaneManifests(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("HTTP_PROXY", "http://proxy.example.invalid:3128")
	t.Setenv("NO_PROXY", "example.invalid")

	after, err := ControlPlaneManifests(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("rendered manifests changed when the render process's proxy env vars changed (ambient leak)")
	}
	for _, f := range after {
		if bytes.Contains(f.Content, []byte("proxy.example.invalid")) {
			t.Errorf("%s: leaked the render host's HTTP_PROXY value into the manifest", f.Path)
		}
	}
}

// TestControlPlaneManifests_NoAmbientCACertMounts guards against
// kubeadm's getHostPathVolumesForTheControlPlane, which os.Stat's a
// fixed list of render-host paths (see ambientCACertMountPaths) and
// conditionally mounts whichever exist. Our construction in
// manifests.go must never emit these mounts in the first place (no
// generate-then-strip pass exists); this test guards that. It is not
// merely a hypothetical: this dev host actually has two of the five
// (/etc/pki/ca-trust, /etc/pki/tls/certs) present.
func TestControlPlaneManifests_NoAmbientCACertMounts(t *testing.T) {
	cfg := defaultedInit(t)
	cfg.LocalAPIEndpoint.AdvertiseAddress = "192.0.2.10"

	files, err := ControlPlaneManifests(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		for _, path := range ambientCACertMountPaths {
			if bytes.Contains(f.Content, []byte(path)) {
				t.Errorf("%s: contains ambient render-host mount %q (must be stripped)", f.Path, path)
			}
		}
	}
	// The always-present, non-ambient mount must survive.
	apiserver := files[1]
	if !bytes.Contains(apiserver.Content, []byte("/etc/ssl/certs")) {
		t.Error("apiserver manifest missing the unconditional /etc/ssl/certs mount (over-stripped?)")
	}
}

func TestControlPlaneDesired_ContainsAllClasses(t *testing.T) {
	cfg := defaultedInit(t)
	d, err := ControlPlaneDesired(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, f := range d.Files {
		paths[f.Path] = true
	}
	for _, p := range []string{
		KubeletConfigPath,
		KubeletFlagsEnvPath,
		"etc/kubernetes/manifests/etcd.yaml",
		"etc/kubernetes/pki/ca.key",
		"etc/kubernetes/admin.conf",
	} {
		if !paths[p] {
			t.Errorf("ControlPlaneDesired missing %s", p)
		}
	}
}

func TestWorkerDesired_HasNoCPMaterial(t *testing.T) {
	cfg := defaultedInit(t)
	d, err := WorkerDesired(cfg, []byte("apiVersion: v1\nkind: Config\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range d.Files {
		if strings.HasPrefix(f.Path, ManifestsPathPrefix) || strings.HasPrefix(f.Path, "etc/kubernetes/pki/") {
			t.Errorf("worker desired must not contain %s", f.Path)
		}
	}
	paths := map[string]fs.FileMode{}
	for _, f := range d.Files {
		paths[f.Path] = f.Mode
	}
	if m, ok := paths["etc/kubernetes/bootstrap-kubelet.conf"]; !ok || m != 0o600 {
		t.Errorf("bootstrap-kubelet.conf missing or wrong mode %o", m)
	}
	if _, ok := paths[KubeletConfigPath]; !ok {
		t.Error("worker desired missing kubelet config")
	}
	if _, ok := paths[KubeletFlagsEnvPath]; !ok {
		t.Error("worker desired missing kubelet flags env")
	}
}

func TestDesired_RejectsOversizedRender(t *testing.T) {
	// The rendered set is size-capped at render time, on the writer's
	// side — unbounded user content (files:, extra args) must fail
	// here, not wedge every subsequent write (an OCPBUGS-62619-shaped
	// failure).
	cfg := defaultedInit(t)
	cfg.NodeRegistration.KubeletExtraArgs = []kubeadmapi.Arg{{Name: "big", Value: strings.Repeat("x", MaxRenderedBytes)}}
	if _, err := WorkerDesired(cfg, nil); err == nil {
		t.Fatal("oversized render must be rejected at assembly")
	}
}
