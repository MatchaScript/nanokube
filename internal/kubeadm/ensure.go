package kubeadm

import (
	"fmt"
	"os"

	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/componentconfigs"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/controlplane"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/etcd"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubelet"
	"k8s.io/utils/ptr"

	"github.com/MatchaScript/nanokube/internal/layout"
)

// KubeletResolverConfig is pinned explicitly so rendered kubelet
// config bytes never depend on the machine doing the rendering:
// kubeadm's own defaulting (componentconfigs' mutateResolverConfig)
// probes whether systemd-resolved is active on the local host and
// fills resolvConf from the answer — but only when the field is nil.
// Nodes always run systemd-resolved (homelab bootc images), so the
// resolved stub path is the correct value for every node, and setting
// it up front makes the host probe irrelevant.
const KubeletResolverConfig = "/run/systemd/resolve/resolv.conf"

// PinKubeletResolverConfig sets ResolverConfig on cfg's kubelet
// component config to KubeletResolverConfig. Every kubelet-config
// writer — ensureKubeletFiles here and render.KubeletConfig — must
// call this before kubelet.WriteConfigToDisk so their outputs stay
// byte-identical on any host, systemd-resolved or not.
func PinKubeletResolverConfig(cfg *kubeadmapi.ClusterConfiguration) error {
	kubeletCfg, ok := cfg.ComponentConfigs[componentconfigs.KubeletGroup]
	if !ok {
		return fmt.Errorf("no kubelet component config found")
	}
	kc, ok := kubeletCfg.Get().(*kubeletconfigv1beta1.KubeletConfiguration)
	if !ok {
		return fmt.Errorf("unexpected kubelet component config type %T", kubeletCfg.Get())
	}
	kc.ResolverConfig = ptr.To(KubeletResolverConfig)
	kubeletCfg.Set(kc)
	return nil
}

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
func Ensure(cfg *kubeadmapi.InitConfiguration, l layout.Layout) error {
	for _, dir := range []string{l.ManifestsDir, l.KubeletDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	// Pin CertificatesDir on a private copy of cfg (was ApplyLayout's job).
	own := *cfg
	own.CertificatesDir = l.PKIDir
	cfg = &own

	const patchesDir = ""
	const isDryRun = false

	if err := etcd.CreateLocalEtcdStaticPodManifestFile(
		l.ManifestsDir, patchesDir, cfg.NodeRegistration.Name, &cfg.ClusterConfiguration, &cfg.LocalAPIEndpoint, isDryRun,
	); err != nil {
		return fmt.Errorf("create etcd manifest: %w", err)
	}

	if err := controlplane.CreateInitStaticPodManifestFiles(
		l.ManifestsDir, patchesDir, cfg, isDryRun,
	); err != nil {
		return fmt.Errorf("create control plane manifests: %w", err)
	}

	return ensureKubeletFiles(cfg, l)
}

// EnsureWorker renders only what a worker needs on every boot: the
// kubelet env file, instance config, and kubelet config. No static
// pods, no etcd — a worker's control plane is remote.
func EnsureWorker(cfg *kubeadmapi.InitConfiguration, l layout.Layout) error {
	if err := os.MkdirAll(l.KubeletDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", l.KubeletDir, err)
	}
	own := *cfg
	own.CertificatesDir = l.PKIDir
	return ensureKubeletFiles(&own, l)
}

// ensureKubeletFiles writes the three kubelet config artifacts in the
// order kubeadm requires: env file → instance config → main config.
// WriteConfigToDisk reads the instance file as a patch when the
// NodeLocalCRISocket feature gate is on (GA+locked as of k8s v1.36),
// so the instance file must be written first.
func ensureKubeletFiles(cfg *kubeadmapi.InitConfiguration, l layout.Layout) error {
	const patchesDir = ""
	if err := kubelet.WriteKubeletDynamicEnvFile(&cfg.ClusterConfiguration, &cfg.NodeRegistration, false, l.KubeletDir); err != nil {
		return fmt.Errorf("write kubelet env file: %w", err)
	}
	instance := &kubeletconfigv1beta1.KubeletConfiguration{
		ContainerRuntimeEndpoint: cfg.NodeRegistration.CRISocket,
	}
	if err := kubelet.WriteInstanceConfigToDisk(instance, l.KubeletDir); err != nil {
		return fmt.Errorf("write kubelet instance config: %w", err)
	}
	if err := PinKubeletResolverConfig(&cfg.ClusterConfiguration); err != nil {
		return fmt.Errorf("pin kubelet resolvConf: %w", err)
	}
	if err := kubelet.WriteConfigToDisk(&cfg.ClusterConfiguration, l.KubeletDir, patchesDir, os.Stderr); err != nil {
		return fmt.Errorf("write kubelet config: %w", err)
	}
	return nil
}
