package render

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	"github.com/MatchaScript/nanokube/internal/kubeadm"
	"github.com/MatchaScript/nanokube/internal/layouttest"
)

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
