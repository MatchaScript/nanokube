package certs

import (
	"fmt"
	"os"

	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/certs"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/certs/renewal"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubeconfig"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/kubeadm"
)

// Signer issues and renews the cert material under Layout.PKIDir +
// Layout.KubeconfigDir. Construct one per operation; instances are
// cheap and stateless beyond the captured cfg/layout/nodeName.
type Signer struct {
	cfg      *v1alpha1.NanoKubeConfig
	layout   Layout
	nodeName string
}

// NewSigner returns a Signer scoped to the supplied layout. nodeName is
// folded into kubeadm's InitConfiguration so that node-scoped fields
// (kubelet client cert CN, kubelet kubeconfig server URL, etc.) are
// populated correctly.
func NewSigner(cfg *v1alpha1.NanoKubeConfig, layout Layout, nodeName string) *Signer {
	return &Signer{cfg: cfg, layout: layout, nodeName: nodeName}
}

// kubeadmLayout converts our Layout into kubeadm.Layout so that
// BuildInitConfiguration receives PKIDir/KubeconfigDir wired to the
// same paths. The Manifests/Kubelet fields are unused by the cert path
// and left blank — kubeadm.Ensure (called separately) supplies its own.
func (s *Signer) kubeadmLayout() kubeadm.Layout {
	return kubeadm.Layout{
		PKIDir:        s.layout.PKIDir,
		KubeconfigDir: s.layout.KubeconfigDir,
	}
}

// EnsureAll generates whatever CAs and leaf certificates are missing
// under PKIDir, plus the four reconcilable kubeconfig files (admin,
// controller-manager, scheduler, kubelet) under KubeconfigDir.
//
// Existing files are validated and reused; an existing file that fails
// validation is a hard error (we deliberately do NOT silently overwrite
// a corrupt CA). super-admin.conf is intentionally not produced —
// initialize.WriteSuperAdminKubeconfig is the sole writer.
func (s *Signer) EnsureAll() error {
	kc, err := kubeadm.BuildInitConfiguration(s.cfg, s.kubeadmLayout(), s.nodeName)
	if err != nil {
		return err
	}

	for _, dir := range []string{s.layout.PKIDir, s.layout.KubeconfigDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	if err := certs.CreatePKIAssets(kc); err != nil {
		return fmt.Errorf("create PKI assets: %w", err)
	}

	for _, name := range []string{
		kubeadmconstants.AdminKubeConfigFileName,
		kubeadmconstants.ControllerManagerKubeConfigFileName,
		kubeadmconstants.SchedulerKubeConfigFileName,
		kubeadmconstants.KubeletKubeConfigFileName,
	} {
		if err := kubeconfig.CreateKubeConfigFile(name, s.layout.KubeconfigDir, kc); err != nil {
			return fmt.Errorf("create kubeconfig %s: %w", name, err)
		}
	}

	return nil
}

// renewalManager is a small helper used by RenewLeaves and CheckLeaves.
// Instantiated lazily because it loads the CA off disk.
func (s *Signer) renewalManager() (*renewal.Manager, error) {
	kc, err := kubeadm.BuildInitConfiguration(s.cfg, s.kubeadmLayout(), s.nodeName)
	if err != nil {
		return nil, err
	}
	mgr, err := renewal.NewManager(&kc.ClusterConfiguration, s.layout.KubeconfigDir)
	if err != nil {
		return nil, fmt.Errorf("renewal manager: %w", err)
	}
	return mgr, nil
}

// RenewLeaves re-signs the listed leaf certificates using the existing
// CA on disk. Each name must be a LeafKind constant; super-admin.conf
// is accepted only when the file already exists, since the renewal
// manager loads the current cert before re-issuing.
//
// Backed by kubeadm's renewal.Manager.RenewUsingLocalCA, which handles
// both PKI cert files (apiserver.crt, etcd/server.crt, …) and
// kubeconfig-embedded client certs (admin.conf, scheduler.conf, …)
// behind a single name-keyed lookup.
func (s *Signer) RenewLeaves(leaves []LeafKind) error {
	mgr, err := s.renewalManager()
	if err != nil {
		return err
	}
	for _, leaf := range leaves {
		renewed, err := mgr.RenewUsingLocalCA(string(leaf))
		if err != nil {
			return fmt.Errorf("renew %s: %w", leaf, err)
		}
		if !renewed {
			return fmt.Errorf("renew %s: skipped (externally managed CA — cannot renew without CA key)", leaf)
		}
	}
	return nil
}
