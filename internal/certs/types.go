// Package certs owns the lifecycle of /etc/kubernetes/pki and the
// kubeconfig-embedded client certificates: initial issuance during
// `nanokube init` (with optional BYOCA seeding from /etc/nanokube/certs)
// and per-boot leaf renewal driven by the kubeadm renewal manager.
//
// The package deliberately does NOT cover CA renewal: CA refresh is the
// operator's responsibility (image rebuild + `nanokube reset`). Limiting
// the automation surface to leaf renewal mirrors `kubeadm certs renew`'s
// own scope and avoids cascading every leaf on every CA bump.
package certs

import "time"

// Layout selects the on-disk locations the certs package reads/writes.
// Production callers populate it from the paths package; tests use
// t.TempDir-derived values.
type Layout struct {
	// PKIDir is /etc/kubernetes/pki — kubeadm's canonical certificatesDir
	// and the destination of every CA copy + leaf issuance/renewal.
	PKIDir string
	// KubeconfigDir is /etc/kubernetes — where admin.conf / scheduler.conf
	// etc. live. The renewal manager writes back into the same files when
	// renewing the embedded client cert.
	KubeconfigDir string
	// OperatorDir is /etc/nanokube/certs — the optional BYOCA seed source.
	// Files placed here are copied into PKIDir during `nanokube init`;
	// absent files trigger self-signing.
	OperatorDir string
}

// LeafKind enumerates every renewable leaf certificate the kubeadm
// renewal manager recognises. Values match the renewal manager's
// internal name keys 1:1 (see kubeadm certs/renewal/manager.go), so a
// LeafKind can be passed straight to RenewUsingLocalCA.
//
// kubelet.conf is intentionally absent: kubelet rotates its client cert
// via the CSR API and external code must not touch it.
type LeafKind string

const (
	LeafAPIServer              LeafKind = "apiserver"
	LeafAPIServerKubeletClient LeafKind = "apiserver-kubelet-client"
	LeafAPIServerEtcdClient    LeafKind = "apiserver-etcd-client"
	LeafFrontProxyClient       LeafKind = "front-proxy-client"
	LeafEtcdServer             LeafKind = "etcd-server"
	LeafEtcdPeer               LeafKind = "etcd-peer"
	LeafEtcdHealthcheckClient  LeafKind = "etcd-healthcheck-client"
	LeafAdminConf              LeafKind = "admin.conf"
	LeafSuperAdminConf         LeafKind = "super-admin.conf"
	LeafControllerManagerConf  LeafKind = "controller-manager.conf"
	LeafSchedulerConf          LeafKind = "scheduler.conf"
)

// AllLeaves returns every LeafKind value in a stable order. Used by
// CheckLeaves and by tests asserting the full leaf set.
func AllLeaves() []LeafKind {
	return []LeafKind{
		LeafAPIServer,
		LeafAPIServerKubeletClient,
		LeafAPIServerEtcdClient,
		LeafFrontProxyClient,
		LeafEtcdServer,
		LeafEtcdPeer,
		LeafEtcdHealthcheckClient,
		LeafAdminConf,
		LeafSuperAdminConf,
		LeafControllerManagerConf,
		LeafSchedulerConf,
	}
}

// RenewalThreshold is the leaf-cert remaining-lifetime window below
// which lifecycle.Boot triggers a renewal before starting kubelet.
// 4 months ≈ residual 33% on the default 1-year leaf, which is
// generous enough that a host that boots monthly never approaches
// expiry, while never renewing more than once per leaf-validity window.
const RenewalThreshold = 120 * 24 * time.Hour
