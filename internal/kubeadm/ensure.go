package kubeadm

import (
	"fmt"
	"os"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/controlplane"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/etcd"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubelet"
)

// Ensure runs the post-cert kubeadm phases that must hold true on every
// boot in dependency order:
//
//	etcd static pod -> control plane static pods -> kubelet config
//
// PKI and kubeconfig generation moved to package internal/certs:
// `nanokube init` calls certs.Init beforehand, and `nanokube boot`
// trusts the on-disk PKI from a prior init. Each phase below is
// internally idempotent — existing valid files are preserved.
//
// Note: super-admin.conf is deliberately NOT touched here.
// initialize.WriteSuperAdminKubeconfig is the sole writer; recreating
// it in a per-boot reconcile would defeat its post-init deletion.
func Ensure(cfg *kubeadmapi.InitConfiguration, layout Layout) error {
	for _, dir := range []string{layout.ManifestsDir, layout.KubeletDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	const patchesDir = ""
	const isDryRun = false

	if err := etcd.CreateLocalEtcdStaticPodManifestFile(
		layout.ManifestsDir, patchesDir, cfg.NodeRegistration.Name, &cfg.ClusterConfiguration, &cfg.LocalAPIEndpoint, isDryRun,
	); err != nil {
		return fmt.Errorf("create etcd manifest: %w", err)
	}

	if err := controlplane.CreateInitStaticPodManifestFiles(
		layout.ManifestsDir, patchesDir, cfg, isDryRun,
	); err != nil {
		return fmt.Errorf("create control plane manifests: %w", err)
	}

	// kubelet phase: ordering mirrors kubeadm's init kubelet-start.
	// WriteConfigToDisk reads the instance file as a patch when the
	// NodeLocalCRISocket feature gate is on (GA+locked as of k8s v1.36),
	// so the instance file must be written first.
	if err := kubelet.WriteKubeletDynamicEnvFile(&cfg.ClusterConfiguration, &cfg.NodeRegistration, false, layout.KubeletDir); err != nil {
		return fmt.Errorf("write kubelet env file: %w", err)
	}
	instance := &kubeletconfigv1beta1.KubeletConfiguration{
		ContainerRuntimeEndpoint: cfg.NodeRegistration.CRISocket,
	}
	if err := kubelet.WriteInstanceConfigToDisk(instance, layout.KubeletDir); err != nil {
		return fmt.Errorf("write kubelet instance config: %w", err)
	}
	if err := kubelet.WriteConfigToDisk(&cfg.ClusterConfiguration, layout.KubeletDir, patchesDir, os.Stderr); err != nil {
		return fmt.Errorf("write kubelet config: %w", err)
	}

	return nil
}
