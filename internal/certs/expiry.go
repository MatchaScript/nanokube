package certs

import (
	"fmt"
	"path/filepath"
	"time"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
)

// LeafExpiry is one entry in CheckLeaves' report. NotFound is true when
// the underlying file is absent (e.g. super-admin.conf during a
// reconcile boot); in that case NotAfter / Remaining are zero and the
// entry must be skipped by renewal logic, not treated as expired.
type LeafExpiry struct {
	Path      string
	NotAfter  time.Time
	Remaining time.Duration
	NotFound  bool
}

// CheckLeaves returns one LeafExpiry per LeafKind, reading the current
// state of layout.PKIDir and layout.KubeconfigDir via the kubeadm
// renewal manager. Missing leaves are reported with NotFound=true
// rather than as an error so callers can skip them without special-
// casing super-admin.conf and other conditionally-present files.
func CheckLeaves(cfg *v1alpha1.NanoKubeConfig, layout Layout, nodeName string) (map[LeafKind]LeafExpiry, error) {
	signer := NewSigner(cfg, layout, nodeName)
	mgr, err := signer.renewalManager()
	if err != nil {
		return nil, err
	}

	out := make(map[LeafKind]LeafExpiry, len(AllLeaves()))
	for _, leaf := range AllLeaves() {
		path := leafPath(layout, leaf)
		exists, err := pathExists(path)
		if err != nil {
			return nil, err
		}
		if !exists {
			out[leaf] = LeafExpiry{Path: path, NotFound: true}
			continue
		}
		info, err := mgr.GetCertificateExpirationInfo(string(leaf))
		if err != nil {
			return nil, fmt.Errorf("expiry info for %s: %w", leaf, err)
		}
		out[leaf] = LeafExpiry{
			Path:      path,
			NotAfter:  info.ExpirationDate,
			Remaining: time.Until(info.ExpirationDate),
		}
	}
	return out, nil
}

// leafPath maps a LeafKind back to the on-disk file the renewal manager
// reads/writes. PKI certs land under PKIDir (with the etcd/ subfolder
// for etcd-* kinds); kubeconfigs are flat under KubeconfigDir.
func leafPath(layout Layout, leaf LeafKind) string {
	switch leaf {
	case LeafAPIServer:
		return filepath.Join(layout.PKIDir, "apiserver.crt")
	case LeafAPIServerKubeletClient:
		return filepath.Join(layout.PKIDir, "apiserver-kubelet-client.crt")
	case LeafAPIServerEtcdClient:
		return filepath.Join(layout.PKIDir, "apiserver-etcd-client.crt")
	case LeafFrontProxyClient:
		return filepath.Join(layout.PKIDir, "front-proxy-client.crt")
	case LeafEtcdServer:
		return filepath.Join(layout.PKIDir, "etcd", "server.crt")
	case LeafEtcdPeer:
		return filepath.Join(layout.PKIDir, "etcd", "peer.crt")
	case LeafEtcdHealthcheckClient:
		return filepath.Join(layout.PKIDir, "etcd", "healthcheck-client.crt")
	case LeafAdminConf, LeafSuperAdminConf, LeafControllerManagerConf, LeafSchedulerConf:
		return filepath.Join(layout.KubeconfigDir, string(leaf))
	}
	// Unreachable: every LeafKind constant is enumerated above. A new
	// constant added without updating this switch is a programming error.
	panic("unhandled LeafKind: " + string(leaf))
}
