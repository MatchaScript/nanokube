package certs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/certs"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/certs/renewal"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubeconfig"
)

// Signer issues and renews the cert material under Layout.PKIDir +
// Layout.KubeconfigDir. Construct one per operation; instances are
// cheap and stateless beyond the captured cfg/layout.
type Signer struct {
	cfg    *kubeadmapi.InitConfiguration
	layout Layout
}

// NewSigner returns a Signer scoped to the supplied layout. The kubeadm
// InitConfiguration's CertificatesDir is normalised to layout.PKIDir
// on a private copy so callers can reuse cfg with a different layout
// elsewhere without aliasing problems.
func NewSigner(cfg *kubeadmapi.InitConfiguration, layout Layout) *Signer {
	own := *cfg // shallow copy; ClusterConfiguration is value-embedded
	own.CertificatesDir = layout.PKIDir
	return &Signer{cfg: &own, layout: layout}
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
	for _, dir := range []string{s.layout.PKIDir, s.layout.KubeconfigDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	if err := certs.CreatePKIAssets(s.cfg); err != nil {
		return fmt.Errorf("create PKI assets: %w", err)
	}

	for _, name := range []string{
		kubeadmconstants.AdminKubeConfigFileName,
		kubeadmconstants.ControllerManagerKubeConfigFileName,
		kubeadmconstants.SchedulerKubeConfigFileName,
		kubeadmconstants.KubeletKubeConfigFileName,
	} {
		if err := kubeconfig.CreateKubeConfigFile(name, s.layout.KubeconfigDir, s.cfg); err != nil {
			return fmt.Errorf("create kubeconfig %s: %w", name, err)
		}
	}

	return nil
}

// renewalManager is a small helper used by RenewLeaves and CheckLeaves.
// Instantiated lazily because it loads the CA off disk.
func (s *Signer) renewalManager() (*renewal.Manager, error) {
	mgr, err := renewal.NewManager(&s.cfg.ClusterConfiguration, s.layout.KubeconfigDir)
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
			// Should be unreachable: nanokube always owns the CA key.
			// A false return here means the kubeadm manager could not
			// find a usable local CA, which is a programming error
			// (PKI was not initialised) rather than a renewal outcome.
			return fmt.Errorf("renew %s: renewal manager reported no-op despite local CA ownership", leaf)
		}
	}
	return nil
}

// RegenerateCA deletes the named CA's .crt + .key together with every
// PKI leaf signed by it and every kubeconfig whose embedded client
// cert chains back to it, then re-runs kubeadm's CreatePKIAssets phase
// (which re-creates the missing CA and re-signs the missing leaves)
// followed by CreateKubeConfigFile for each affected kubeconfig.
//
// super-admin.conf is intentionally NOT regenerated: it is a
// break-glass cred whose presence on a long-lived node is itself an
// invariant violation, and `nanokube kubeconfig super-admin` is the
// documented path to re-issue it under the new CA.
func (s *Signer) RegenerateCA(ca CAKind) error {
	toDelete := []string{
		caCertPath(s.layout, ca),
		caKeyPath(s.layout, ca),
	}
	for _, leaf := range dependentLeaves(ca) {
		toDelete = append(toDelete, leafPath(s.layout, leaf))
		if k := leafKeyPath(s.layout, leaf); k != "" {
			toDelete = append(toDelete, k)
		}
	}
	kubeconfigs := dependentKubeconfigs(ca)
	for _, name := range kubeconfigs {
		toDelete = append(toDelete, filepath.Join(s.layout.KubeconfigDir, name))
	}

	for _, p := range toDelete {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}

	if err := certs.CreatePKIAssets(s.cfg); err != nil {
		return fmt.Errorf("create PKI assets for CA %s: %w", ca, err)
	}

	for _, name := range kubeconfigs {
		if err := kubeconfig.CreateKubeConfigFile(name, s.layout.KubeconfigDir, s.cfg); err != nil {
			return fmt.Errorf("create kubeconfig %s: %w", name, err)
		}
	}

	return nil
}
