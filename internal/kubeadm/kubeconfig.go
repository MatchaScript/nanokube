package kubeadm

import (
	"fmt"

	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubeconfig"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
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
func WriteSuperAdminKubeconfig(cfg *v1alpha1.NanoKubeConfig, layout Layout, nodeName string) error {
	kc, err := BuildInitConfiguration(cfg, layout, nodeName)
	if err != nil {
		return err
	}
	if err := kubeconfig.CreateKubeConfigFile(
		kubeadmconstants.SuperAdminKubeConfigFileName, layout.KubeconfigDir, kc,
	); err != nil {
		return fmt.Errorf("create super-admin kubeconfig: %w", err)
	}
	return nil
}
