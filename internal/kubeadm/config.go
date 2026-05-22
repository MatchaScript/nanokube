// Package kubeadm carries nanokube's adaptation of kubeadm's static-pod
// and bootstrap phases. After config.Load parses the kubeadm
// InitConfiguration from the user's multi-document YAML, the helpers
// here drive kubeadm's own certs / controlplane / etcd / kubelet
// phases against that already-defaulted, already-validated object.
package kubeadm

import (
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"

	"github.com/MatchaScript/nanokube/internal/paths"
)

// Layout selects the on-disk directories that kubeadm phases write to.
// Production callers pass DefaultLayout; tests use t.TempDir-derived
// values.
type Layout struct {
	PKIDir        string
	KubeconfigDir string
	ManifestsDir  string
	KubeletDir    string
}

// DefaultLayout returns the production layout rooted at /etc/kubernetes
// and /var/lib/kubelet.
func DefaultLayout() Layout {
	return Layout{
		PKIDir:        paths.PKIDir,
		KubeconfigDir: paths.KubeconfigDir,
		ManifestsDir:  paths.ManifestsDir,
		KubeletDir:    paths.KubeletDir,
	}
}

// ApplyLayout overrides the on-config field that nanokube manages out of
// band of user input. CertificatesDir is the only InitConfiguration field
// that contains a filesystem path; the rest of Layout (ManifestsDir,
// KubeletDir, KubeconfigDir) is passed to phase functions directly.
//
// Called by config.Load with DefaultLayout so production reads see a
// uniform path; tests call it with their per-test temp layout.
func ApplyLayout(cfg *kubeadmapi.InitConfiguration, layout Layout) {
	cfg.CertificatesDir = layout.PKIDir
}
