// Package kubeadm adapts nanokube configuration to the Go API exposed by
// k8s.io/kubernetes/cmd/kubeadm/app. nanokube reuses kubeadm phases
// (certs, kubeconfig, controlplane, etcd, kubelet) as a library rather
// than shelling out to the kubeadm CLI. The orchestration mirrors the
// approach used by vcluster (pkg/certs/ensure.go).
package kubeadm

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/componentconfigs"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/paths"
	"github.com/MatchaScript/nanokube/internal/version"
)

// Layout selects the on-disk directories used by Ensure. Production callers
// pass the paths defaults; tests use t.TempDir-derived values.
type Layout struct {
	PKIDir        string // where certs.CreatePKIAssets writes
	KubeconfigDir string // where kubeconfig.CreateJoinControlPlaneKubeConfigFiles writes
	ManifestsDir  string // where static pod manifests land
	KubeletDir    string // where kubelet config.yaml lands
}

// DefaultLayout returns the production layout rooted at /etc/kubernetes
// and /var/lib/kubelet.
func DefaultLayout() Layout {
	return Layout{
		PKIDir:        paths.PKIDir,
		KubeconfigDir: paths.KubeconfigDir,
		ManifestsDir:  paths.ManifestsDir,
		KubeletDir:    paths.KubeletDir,
	}
}

// BuildInitConfiguration translates a NanoKubeConfig into the kubeadm
// InitConfiguration consumed by every phase below. It is exported so that
// future operator-style callers (k0smotron-equivalent) can inject extra
// overrides before calling phases themselves.
func BuildInitConfiguration(cfg *v1alpha1.NanoKubeConfig, layout Layout, nodeName string) (*kubeadmapi.InitConfiguration, error) {
	kc, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		return nil, fmt.Errorf("kubeadm default init config: %w", err)
	}

	kc.ClusterName = "kubernetes"
	kc.KubernetesVersion = version.KubernetesVersion
	kc.CertificatesDir = layout.PKIDir

	kc.NodeRegistration.Name = nodeName
	kc.NodeRegistration.CRISocket = cfg.Spec.Runtime.CRISocket
	// Taints come from config (SetDefaults fills in the standard control-plane
	// taint when the user leaves the field unset). An explicit empty slice
	// from the user means "no taints"; we must preserve that by overriding
	// kubeadm's own defaulting, which would otherwise inject the CP taint.
	kc.NodeRegistration.Taints = cfg.Spec.NodeRegistration.Taints

	kc.LocalAPIEndpoint.AdvertiseAddress = cfg.Spec.ControlPlane.AdvertiseAddress
	kc.LocalAPIEndpoint.BindPort = cfg.Spec.ControlPlane.BindPort

	kc.Networking.ServiceSubnet = cfg.Spec.ControlPlane.ServiceSubnet
	kc.Networking.PodSubnet = cfg.Spec.ControlPlane.PodSubnet
	kc.Networking.DNSDomain = "cluster.local"

	if kc.Etcd.Local != nil {
		kc.Etcd.Local.ServerCertSANs = extraSANs(cfg)
		kc.Etcd.Local.PeerCertSANs = extraSANs(cfg)
	}

	kc.APIServer.CertSANs = extraSANs(cfg)

	day := 24 * time.Hour
	kc.CACertificateValidityPeriod = &metav1.Duration{Duration: time.Duration(cfg.Spec.Certificates.CAValidityDays) * day}
	kc.CertificateValidityPeriod = &metav1.Duration{Duration: time.Duration(cfg.Spec.Certificates.LeafValidityDays) * day}

	// ClusterDNS and CgroupDriver live on the embedded KubeletConfiguration,
	// not NodeRegistration. DefaultedStaticInitConfiguration already populated
	// the component config with values derived from the *default* ServiceSubnet,
	// so we overwrite them with the user's choices after Networking is set.
	if kcc, ok := kc.ComponentConfigs[componentconfigs.KubeletGroup]; ok {
		kubeletCfg := kcc.Get().(*kubeletconfigv1beta1.KubeletConfiguration)
		kubeletCfg.ClusterDNS = []string{cfg.Spec.ControlPlane.ClusterDNS}
		kubeletCfg.ClusterDomain = kc.Networking.DNSDomain
		kubeletCfg.CgroupDriver = string(cfg.Spec.Runtime.CgroupDriver)
	}

	return kc, nil
}

// extraSANs returns the user-declared SANs verbatim. kubeadm's phases/certs
// classifies each entry as DNS or IP internally, so we do not pre-split.
func extraSANs(cfg *v1alpha1.NanoKubeConfig) []string {
	out := make([]string, len(cfg.Spec.Certificates.ExtraSANs))
	copy(out, cfg.Spec.Certificates.ExtraSANs)
	return out
}
