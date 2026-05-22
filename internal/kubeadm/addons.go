package kubeadm

import (
	"fmt"
	"io"

	"k8s.io/client-go/kubernetes"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/addons/dns"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/addons/proxy"
)

// EnsureAddons deploys the CoreDNS and kube-proxy addons to a running
// apiserver. CNI is deliberately out of scope; operators are expected
// to apply their preferred plugin separately.
//
// Safe to re-run: both kubeadm phases reconcile via server-side apply,
// so repeated invocations converge to the desired state without churn.
func EnsureAddons(cfg *kubeadmapi.InitConfiguration, client kubernetes.Interface, out io.Writer) error {
	const (
		patchesDir    = ""
		printManifest = false
	)
	if err := dns.EnsureDNSAddon(&cfg.ClusterConfiguration, client, patchesDir, out, printManifest); err != nil {
		return fmt.Errorf("ensure CoreDNS: %w", err)
	}
	if err := proxy.EnsureProxyAddon(&cfg.ClusterConfiguration, &cfg.LocalAPIEndpoint, client, out, printManifest); err != nil {
		return fmt.Errorf("ensure kube-proxy: %w", err)
	}
	return nil
}
