package v1alpha1

import (
	"strings"
	"testing"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"

	"github.com/MatchaScript/nanokube/internal/paths"
	"github.com/MatchaScript/nanokube/internal/version"
)

func validInputs() (*NanoKubeConfig, *kubeadmapi.InitConfiguration) {
	wrapper := NewDefault()
	kc := &kubeadmapi.InitConfiguration{}
	kc.ClusterConfiguration.KubernetesVersion = version.KubernetesVersion
	kc.ClusterConfiguration.CertificatesDir = paths.PKIDir
	return wrapper, kc
}

func TestValidate_AcceptsCanonicalInputs(t *testing.T) {
	if err := Validate(validInputs()); err != nil {
		t.Fatalf("Validate(canonical) = %v; want nil", err)
	}
}

func TestValidate_RejectsWrongAPIVersion(t *testing.T) {
	wrapper, kc := validInputs()
	wrapper.APIVersion = "bootstrap.example.com/v1"
	err := Validate(wrapper, kc)
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("Validate = %v; want apiVersion error", err)
	}
}

func TestValidate_RejectsWrongKind(t *testing.T) {
	wrapper, kc := validInputs()
	wrapper.Kind = "NotNanoKubeConfig"
	err := Validate(wrapper, kc)
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("Validate = %v; want kind error", err)
	}
}

func TestValidate_RejectsNilKubeadm(t *testing.T) {
	wrapper := NewDefault()
	err := Validate(wrapper, nil)
	if err == nil || !strings.Contains(err.Error(), "kubeadm") {
		t.Fatalf("Validate = %v; want kubeadm-nil error", err)
	}
}

func TestValidate_RejectsMismatchedKubernetesVersion(t *testing.T) {
	wrapper, kc := validInputs()
	kc.ClusterConfiguration.KubernetesVersion = "v0.0.1-evil"
	err := Validate(wrapper, kc)
	if err == nil || !strings.Contains(err.Error(), "kubernetesVersion") {
		t.Fatalf("Validate = %v; want kubernetesVersion error", err)
	}
}

func TestValidate_AcceptsEmptyKubernetesVersion(t *testing.T) {
	// kubeadm defaults to a non-empty version, but the wrapper must
	// tolerate an empty value too (config.Load fills it in from the
	// image-pinned version before downstream code reads it).
	wrapper, kc := validInputs()
	kc.ClusterConfiguration.KubernetesVersion = ""
	if err := Validate(wrapper, kc); err != nil {
		t.Fatalf("Validate(empty KubernetesVersion) = %v; want nil", err)
	}
}

func TestValidate_RejectsMismatchedCertificatesDir(t *testing.T) {
	wrapper, kc := validInputs()
	kc.ClusterConfiguration.CertificatesDir = "/tmp/elsewhere"
	err := Validate(wrapper, kc)
	if err == nil || !strings.Contains(err.Error(), "certificatesDir") {
		t.Fatalf("Validate = %v; want certificatesDir error", err)
	}
}
