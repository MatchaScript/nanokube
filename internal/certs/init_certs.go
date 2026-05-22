package certs

import (
	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
)

// Init performs first-time PKI provisioning for a fresh node. Every CA
// and leaf is self-signed by nanokube; no operator-supplied material is
// consumed.
//
// Called exclusively from initialize.Run. lifecycle.Boot must NOT
// invoke Init: by then PKIDir is the build artifact and reseeding
// would silently change identities under a running cluster.
func Init(cfg *v1alpha1.NanoKubeConfig, layout Layout, nodeName string) error {
	return NewSigner(cfg, layout, nodeName).EnsureAll()
}
