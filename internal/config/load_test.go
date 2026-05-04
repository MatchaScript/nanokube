package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
)

// writeTempFile drops body into a fresh file under t.TempDir() and returns
// its path.
func writeTempFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func TestLoad_MinimalConfigDefaultsAndValidates(t *testing.T) {
	body := `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
metadata:
  name: local
spec:
  controlPlane:
    advertiseAddress: 192.168.1.10
  certificates:
    selfSigned: true
`
	cfg, err := Load(writeTempFile(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Spec.ControlPlane.BindPort != v1alpha1.DefaultBindPort {
		t.Errorf("defaults not applied; BindPort = %d", cfg.Spec.ControlPlane.BindPort)
	}
	if cfg.Spec.ControlPlane.ServiceSubnet != v1alpha1.DefaultServiceSubnet {
		t.Errorf("ServiceSubnet = %q", cfg.Spec.ControlPlane.ServiceSubnet)
	}
}

func TestLoad_FileNotFoundSurfacesPath(t *testing.T) {
	_, err := Load("/tmp/nanokube-does-not-exist-hopefully")
	if err == nil {
		t.Fatal("Load of missing file = nil")
	}
	if !strings.Contains(err.Error(), "/tmp/nanokube-does-not-exist-hopefully") {
		t.Errorf("error should mention path; got %v", err)
	}
}

// UnmarshalStrict is load-bearing: it prevents typo'd field names (e.g.
// "controllPlane") from silently becoming no-ops. Don't weaken it.
func TestLoad_RejectsUnknownField(t *testing.T) {
	body := `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
spec:
  controlPlane:
    advertiseAddress: 192.168.1.10
  mysterious: value
`
	_, err := Load(writeTempFile(t, body))
	if err == nil {
		t.Fatal("strict parse must reject unknown fields")
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	body := "this: is: not valid: yaml: at all"
	_, err := Load(writeTempFile(t, body))
	if err == nil {
		t.Fatal("Load of malformed yaml = nil")
	}
}

// Load must call Validate after SetDefaults; a semantically invalid
// configuration is rejected even though it parses.
func TestLoad_BubblesValidationError(t *testing.T) {
	body := `apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
spec:
  controlPlane:
    advertiseAddress: "not-an-ip"
  certificates:
    selfSigned: true
`
	_, err := Load(writeTempFile(t, body))
	if err == nil {
		t.Fatal("Load of invalid IP = nil")
	}
	if !strings.Contains(err.Error(), "advertiseAddress") {
		t.Errorf("error should mention advertiseAddress: %v", err)
	}
}

func TestMarshal_RoundTripsThroughLoad(t *testing.T) {
	in := v1alpha1.NewDefault()
	in.Spec.ControlPlane.AdvertiseAddress = "10.20.30.40"
	in.Spec.Certificates.ExtraSANs = []string{"127.0.0.1", "nanokube.local"}

	data, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	path := writeTempFile(t, string(data))
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Marshal: %v", err)
	}

	if out.Spec.ControlPlane.AdvertiseAddress != in.Spec.ControlPlane.AdvertiseAddress {
		t.Errorf("advertiseAddress round-trip: %q -> %q",
			in.Spec.ControlPlane.AdvertiseAddress, out.Spec.ControlPlane.AdvertiseAddress)
	}
	if len(out.Spec.Certificates.ExtraSANs) != 2 {
		t.Errorf("extraSANs lost: got %v", out.Spec.Certificates.ExtraSANs)
	}
}
