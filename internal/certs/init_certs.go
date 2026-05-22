package certs

import (
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
)

// Init performs first-time PKI provisioning for a fresh node. Every CA
// and leaf is self-signed by nanokube; no operator-supplied material is
// consumed.
//
// Called exclusively from initialize.Run. lifecycle.Boot must NOT
// invoke Init: by then PKIDir is the build artifact and reseeding
// would silently change identities under a running cluster.
func Init(cfg *kubeadmapi.InitConfiguration, layout Layout) error {
	return NewSigner(cfg, layout).EnsureAll()
}
