// Package bootstraptoken mints short-lived kubeadm bootstrap tokens for
// `nanokube add-node`. Tokens are minted on demand on a control-plane
// node — `nanokube init` deliberately creates none.
package bootstraptoken

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	certutil "k8s.io/client-go/util/cert"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
	bootstraptokenv1 "k8s.io/kubernetes/cmd/kubeadm/app/apis/bootstraptoken/v1"
	nodebootstraptoken "k8s.io/kubernetes/cmd/kubeadm/app/phases/bootstraptoken/node"
	"k8s.io/kubernetes/cmd/kubeadm/app/util/pubkeypin"
)

// CreateResult carries the operator-facing join credentials.
type CreateResult struct {
	Token      string // id.secret
	CACertHash string // sha256:... (pubkeypin of the cluster CA)
}

// Create mints a bootstrap token with the standard kubeadm node-join
// usages/groups and returns it with the CA public-key pin that
// `nanokube add-node --ca-cert-hash` verifies against.
func Create(client kubernetes.Interface, ttl time.Duration, caCertPath string) (CreateResult, error) {
	raw, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return CreateResult{}, fmt.Errorf("generate token: %w", err)
	}
	bts, err := bootstraptokenv1.NewBootstrapTokenString(raw)
	if err != nil {
		return CreateResult{}, fmt.Errorf("parse generated token: %w", err)
	}
	bt := bootstraptokenv1.BootstrapToken{
		Token:  bts,
		TTL:    &metav1.Duration{Duration: ttl},
		Usages: []string{"authentication", "signing"},
		Groups: []string{"system:bootstrappers:kubeadm:default-node-token"},
	}
	if err := nodebootstraptoken.UpdateOrCreateTokens(client, false, []bootstraptokenv1.BootstrapToken{bt}); err != nil {
		return CreateResult{}, fmt.Errorf("create token secret: %w", err)
	}

	caCerts, err := certutil.CertsFromFile(caCertPath)
	if err != nil {
		return CreateResult{}, fmt.Errorf("load CA cert: %w", err)
	}
	return CreateResult{Token: raw, CACertHash: pubkeypin.Hash(caCerts[0])}, nil
}
