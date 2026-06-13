package kubeadm

import (
	"fmt"

	clientset "k8s.io/client-go/kubernetes"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/bootstraptoken/clusterinfo"
	nodebootstraptoken "k8s.io/kubernetes/cmd/kubeadm/app/phases/bootstraptoken/node"
	kubeletphase "k8s.io/kubernetes/cmd/kubeadm/app/phases/kubelet"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/uploadconfig"
)

// EnsureJoinPrereqs creates or updates the cluster-side objects kubeadm
// init provides for node join and kubelet TLS bootstrap:
//
//   - kubeadm-config + kubelet-config ConfigMaps and their reader RBAC
//     (joined nodes regenerate /etc/nanokube/config.yaml from these)
//   - cluster-info ConfigMap in kube-public + anonymous-read RBAC
//     (token discovery's JWS-signed trust anchor)
//   - the three TLS-bootstrap ClusterRoleBindings
//
// kubeadm:node-autoapprove-certificate-rotation is also what keeps an
// existing SNO's kubelet client cert renewable past its 365d validity —
// without it, rotation CSRs sit Pending forever. Every callee is an
// idempotent kubeadm create-or-update phase, so this runs on init and
// on every control-plane boot.
//
// No bootstrap token is created here: tokens are short-lived join
// credentials minted on demand by `nanokube token create`.
func EnsureJoinPrereqs(cfg *kubeadmapi.InitConfiguration, client clientset.Interface, adminKubeconfig *clientcmdapi.Config) error {
	if err := uploadconfig.UploadConfiguration(cfg, client); err != nil {
		return fmt.Errorf("upload kubeadm-config: %w", err)
	}
	if err := kubeletphase.CreateConfigMap(&cfg.ClusterConfiguration, client); err != nil {
		return fmt.Errorf("upload kubelet-config: %w", err)
	}
	if err := clusterinfo.CreateBootstrapConfigMapIfNotExists(client, adminKubeconfig); err != nil {
		return fmt.Errorf("create cluster-info: %w", err)
	}
	if err := clusterinfo.CreateClusterInfoRBACRules(client); err != nil {
		return fmt.Errorf("cluster-info RBAC: %w", err)
	}
	if err := nodebootstraptoken.AllowBootstrapTokensToPostCSRs(client); err != nil {
		return fmt.Errorf("kubelet-bootstrap CRB: %w", err)
	}
	if err := nodebootstraptoken.AutoApproveNodeBootstrapTokens(client); err != nil {
		return fmt.Errorf("node-autoapprove-bootstrap CRB: %w", err)
	}
	if err := nodebootstraptoken.AutoApproveNodeCertificateRotation(client); err != nil {
		return fmt.Errorf("node-autoapprove-certificate-rotation CRB: %w", err)
	}
	return nil
}
