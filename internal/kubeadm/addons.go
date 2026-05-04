package kubeadm

import (
	"fmt"
	"io"

	"k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/addons/dns"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/addons/proxy"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
)

// EnsureAddons deploys the CoreDNS and kube-proxy addons to a running
// apiserver. CNI is deliberately out of scope; operators are expected to
// apply their preferred plugin separately.
//
// Safe to re-run: both kubeadm phases reconcile via server-side apply, so
// repeated invocations converge to the desired state without churn.
func EnsureAddons(cfg *v1alpha1.NanoKubeConfig, layout Layout, client kubernetes.Interface, out io.Writer) error {
	// Addon phases only read ClusterConfiguration / LocalAPIEndpoint, so the
	// node name is irrelevant here. Pass a placeholder to reuse the shared
	// BuildInitConfiguration path.
	kc, err := BuildInitConfiguration(cfg, layout, "addons")
	if err != nil {
		return err
	}

	const (
		patchesDir    = ""
		printManifest = false
	)
	if err := dns.EnsureDNSAddon(&kc.ClusterConfiguration, client, patchesDir, out, printManifest); err != nil {
		return fmt.Errorf("ensure CoreDNS: %w", err)
	}
	if err := proxy.EnsureProxyAddon(&kc.ClusterConfiguration, &kc.LocalAPIEndpoint, client, out, printManifest); err != nil {
		return fmt.Errorf("ensure kube-proxy: %w", err)
	}
	return nil
}
