package kubeadm

import (
	"fmt"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubeconfig"
)

// WriteSuperAdminKubeconfig (re)writes /etc/kubernetes/super-admin.conf
// from the cluster CA. Two callers:
//
//  1. `nanokube init` (internal/initialize) writes it just-in-time so
//     InitAdminRBAC can authenticate as system:masters to seed the
//     cluster-admins ClusterRoleBinding, then deletes it.
//  2. `nanokube kubeconfig super-admin` regenerates it for break-glass
//     scenarios where RBAC has been broken and admin.conf can no
//     longer reach the apiserver.
//
// Deliberately NOT called from Ensure — super-admin.conf is
// system:masters-bound and bypasses RBAC, so it must not exist on a
// long-lived node. Letting Ensure recreate it on every boot would
// silently undo the deletion in init.
func WriteSuperAdminKubeconfig(cfg *kubeadmapi.InitConfiguration, layout Layout) error {
	if err := kubeconfig.CreateKubeConfigFile(
		kubeadmconstants.SuperAdminKubeConfigFileName, layout.KubeconfigDir, cfg,
	); err != nil {
		return fmt.Errorf("create super-admin kubeconfig: %w", err)
	}
	return nil
}
