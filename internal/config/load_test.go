package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	kubeadmapiv1 "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/v1beta4"
	"k8s.io/kubernetes/cmd/kubeadm/app/componentconfigs"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/layouttest"
)

// writeTempFile drops body into a fresh file under t.TempDir() and
// returns its path.
func writeTempFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

// A minimum NanoKubeConfig wrapper plus the kubeadm InitConfiguration /
// ClusterConfiguration documents nanokube expects. Any field worth
// asserting on per-test is added by the caller via concatenation.
const minimalConfig = `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
metadata:
  name: local
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: 192.168.1.10
nodeRegistration:
  criSocket: unix:///var/run/crio/crio.sock
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
kubernetesVersion: v1.35.0
networking:
  serviceSubnet: 10.96.0.0/12
  podSubnet: 10.244.0.0/16
`

func TestLoad_MinimalConfigParsesAndDefaults(t *testing.T) {
	l := layouttest.New(t)
	loaded, err := Load(writeTempFile(t, minimalConfig), l)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.HasJoin {
		t.Error("HasJoin: got true, want false for init shape")
	}
	cfg := loaded.Init
	if cfg.LocalAPIEndpoint.AdvertiseAddress != "192.168.1.10" {
		t.Errorf("AdvertiseAddress = %q", cfg.LocalAPIEndpoint.AdvertiseAddress)
	}
	if cfg.LocalAPIEndpoint.BindPort != 6443 {
		t.Errorf("BindPort = %d; expected kubeadm default 6443", cfg.LocalAPIEndpoint.BindPort)
	}
	if cfg.CertificatesDir != l.PKIDir {
		t.Errorf("CertificatesDir = %q; want pinned %q", cfg.CertificatesDir, l.PKIDir)
	}
}

func TestLoad_FileNotFoundSurfacesPath(t *testing.T) {
	l := layouttest.New(t)
	_, err := Load("/tmp/nanokube-does-not-exist-hopefully", l)
	if err == nil {
		t.Fatal("Load of missing file = nil")
	}
	if !strings.Contains(err.Error(), "/tmp/nanokube-does-not-exist-hopefully") {
		t.Errorf("error should mention path; got %v", err)
	}
}

func TestLoad_RejectsMissingWrapper(t *testing.T) {
	l := layouttest.New(t)
	// Strip the NanoKubeConfig wrapper out of the minimal config.
	body := strings.SplitN(minimalConfig, "---\n", 2)[1]
	_, err := Load(writeTempFile(t, body), l)
	if err == nil || !strings.Contains(err.Error(), "NanoKubeConfig") {
		t.Fatalf("Load = %v; want NanoKubeConfig-not-found error", err)
	}
}

// joinConfig is the CABPK-style joined-node shape: wrapper +
// JoinConfiguration + ClusterConfiguration, with NO InitConfiguration
// document. NodeRegistration is reproduced by kubeadm's dynamic
// defaulting at load time, so the Join doc carries no node identity.
const joinConfig = `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: JoinConfiguration
discovery:
  file:
    kubeConfigPath: /etc/kubernetes/kubelet.conf
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
kubernetesVersion: v1.35.0
controlPlaneEndpoint: cp.example.test:6443
`

func TestLoad_JoinShape(t *testing.T) {
	l := layouttest.New(t)
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(joinConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(p, l)
	if err != nil {
		t.Fatalf("Load join shape: %v", err)
	}
	if !loaded.HasJoin {
		t.Error("HasJoin: got false, want true")
	}
	if loaded.Init.ControlPlaneEndpoint != "cp.example.test:6443" {
		t.Errorf("controlPlaneEndpoint: got %q", loaded.Init.ControlPlaneEndpoint)
	}
	if loaded.Init.NodeRegistration.Name == "" {
		t.Error("NodeRegistration.Name not dynamically defaulted")
	}
}

func TestLoad_RejectsInitAndJoinTogether(t *testing.T) {
	l := layouttest.New(t)
	// The join shape plus an InitConfiguration document: CABPK either/or
	// is violated, Load must reject it.
	body := joinConfig + `---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: 192.168.1.10
nodeRegistration:
  criSocket: unix:///var/run/crio/crio.sock
`
	_, err := Load(writeTempFile(t, body), l)
	if err == nil || !strings.Contains(err.Error(), "either") {
		t.Fatalf("Load = %v; want either/or rejection error", err)
	}
}

func TestLoad_RejectsMalformedJoinDoc(t *testing.T) {
	l := layouttest.New(t)
	// An unknown field in the JoinConfiguration document must fail the
	// strict unmarshal that validates the doc for well-formedness.
	body := `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: JoinConfiguration
bogus: true
discovery:
  file:
    kubeConfigPath: /etc/kubernetes/kubelet.conf
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
kubernetesVersion: v1.35.0
controlPlaneEndpoint: cp.example.test:6443
`
	_, err := Load(writeTempFile(t, body), l)
	if err == nil || !strings.Contains(err.Error(), "JoinConfiguration") {
		t.Fatalf("Load = %v; want malformed JoinConfiguration error", err)
	}
}

func TestLoad_RejectsCertificatesDirOverride(t *testing.T) {
	l := layouttest.New(t)
	body := minimalConfig + "certificatesDir: /tmp/elsewhere\n"
	_, err := Load(writeTempFile(t, body), l)
	if err == nil || !strings.Contains(err.Error(), "certificatesDir") {
		t.Fatalf("Load = %v; want certificatesDir mismatch error", err)
	}
}

func TestLoad_RejectsMismatchedKubernetesVersion(t *testing.T) {
	l := layouttest.New(t)
	// Use a syntactically valid Kubernetes version that does not match
	// the one pinned in this image — kubeadm itself rejects unparseable
	// values (e.g. v0.0.1-evil) before our gate runs, so the gate-under
	// -test is only reachable with a version kubeadm accepts.
	body := strings.Replace(minimalConfig, "kubernetesVersion: v1.35.0", "kubernetesVersion: v1.34.0", 1)
	_, err := Load(writeTempFile(t, body), l)
	if err == nil || !strings.Contains(err.Error(), "kubernetesVersion") {
		t.Fatalf("Load = %v; want kubernetesVersion mismatch error", err)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	l := layouttest.New(t)
	body := "this: is: not valid: yaml: at all"
	_, err := Load(writeTempFile(t, body), l)
	if err == nil {
		t.Fatal("Load of malformed yaml = nil")
	}
}

// kubeadm's loader (validateSupportedVersion) emits a klog.Warningf for
// deprecated API versions. We don't assert on klog output (capturing
// requires plumbing -log_dir / klog.SetOutput in a test-hostile way);
// instead pin the behaviour that v1beta3 still loads successfully,
// which catches the day kubeadm drops support and Load starts
// returning an error.
func TestLoad_DeprecatedAPIVersionStillLoads(t *testing.T) {
	l := layouttest.New(t)
	body := `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
metadata:
  name: local
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: 192.168.1.10
nodeRegistration:
  criSocket: unix:///var/run/crio/crio.sock
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
kubernetesVersion: v1.35.0
networking:
  serviceSubnet: 10.96.0.0/12
  podSubnet: 10.244.0.0/16
`
	loaded, err := Load(writeTempFile(t, body), l)
	if err != nil {
		t.Fatalf("Load(v1beta3) = %v; v1beta3 should still be accepted with a deprecation warning. "+
			"If kubeadm has dropped v1beta3 support, update this test together with the README to "+
			"document the new minimum supported version.", err)
	}
	if loaded.HasJoin {
		t.Error("HasJoin: got true, want false for init shape")
	}
}

func TestMarshal_RoundTripsThroughLoad(t *testing.T) {
	l := layouttest.New(t)
	in, err := Load(writeTempFile(t, minimalConfig), l)
	if err != nil {
		t.Fatalf("Load(minimal): %v", err)
	}

	// Marshal needs the wrapper too; reconstruct a default one since
	// the loader does not preserve the wrapper (its Spec is empty).
	data, err := Marshal(v1alpha1.NewDefault(), in.Init)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	out, err := Load(writeTempFile(t, string(data)), l)
	if err != nil {
		t.Fatalf("Load after Marshal: %v", err)
	}
	if out.Init.LocalAPIEndpoint.AdvertiseAddress != in.Init.LocalAPIEndpoint.AdvertiseAddress {
		t.Errorf("advertiseAddress round-trip: %q -> %q",
			in.Init.LocalAPIEndpoint.AdvertiseAddress, out.Init.LocalAPIEndpoint.AdvertiseAddress)
	}
}

func TestMarshalJoin_RoundTripsThroughLoad(t *testing.T) {
	l := layouttest.New(t)

	// A defaulted internal config gives us a ClusterConfiguration that
	// already carries the kubelet ComponentConfig, so the round-trip
	// exercises the kubelet-config document path too.
	base, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("DefaultedStaticInitConfiguration: %v", err)
	}
	clusterCfg := base.ClusterConfiguration.DeepCopy()
	clusterCfg.KubernetesVersion = "v1.35.0"
	clusterCfg.ControlPlaneEndpoint = "cp.example.test:6443"

	wrapper := v1alpha1.NewDefault()

	// Build the JoinConfiguration the way a real add-node caller will:
	// through kubeadm's defaulter so Timeouts and the other invariants
	// the serializer relies on are populated. SkipCRIDetect avoids the
	// host CRI probe; the kubeConfigPath must exist on disk because the
	// defaulter validates it, so point at a temp file.
	kubeconfigPath := filepath.Join(t.TempDir(), "kubelet.conf")
	if err := os.WriteFile(kubeconfigPath, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	joinCfg, err := kubeadmconfig.DefaultedJoinConfiguration(
		&kubeadmapiv1.JoinConfiguration{
			Discovery: kubeadmapiv1.Discovery{
				File: &kubeadmapiv1.FileDiscovery{
					KubeConfigPath: kubeconfigPath,
				},
			},
		},
		kubeadmconfig.LoadOrDefaultConfigurationOptions{SkipCRIDetect: true},
	)
	if err != nil {
		t.Fatalf("DefaultedJoinConfiguration: %v", err)
	}

	data, err := MarshalJoin(wrapper, joinCfg, clusterCfg)
	if err != nil {
		t.Fatalf("MarshalJoin: %v", err)
	}

	loaded, err := Load(writeTempFile(t, string(data)), l)
	if err != nil {
		t.Fatalf("Load after MarshalJoin: %v\n--- marshalled ---\n%s", err, data)
	}
	if !loaded.HasJoin {
		t.Error("HasJoin: got false, want true after MarshalJoin round-trip")
	}
	if loaded.Init.ControlPlaneEndpoint != "cp.example.test:6443" {
		t.Errorf("controlPlaneEndpoint round-trip: got %q", loaded.Init.ControlPlaneEndpoint)
	}
	if _, ok := loaded.Init.ClusterConfiguration.ComponentConfigs[componentconfigs.KubeletGroup]; !ok {
		t.Error("kubelet ComponentConfig did not survive MarshalJoin round-trip")
	}
}
