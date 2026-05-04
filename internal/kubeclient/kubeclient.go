// Package kubeclient builds a typed Kubernetes clientset against a
// nanokube-managed control plane. Readiness probes (apiserver /readyz,
// node + control-plane static pod waits) live in package healthcheck;
// keeping kubeclient focused on configuration loading lets the same
// probe implementation back boot, init, and the `nanokube healthcheck`
// CLI without one wrapping the other.
package kubeclient

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// LoadAdmin builds a typed clientset from the kubeconfig at path
// (usually /etc/kubernetes/admin.conf).
func LoadAdmin(path string) (kubernetes.Interface, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("build rest config from %s: %w", path, err)
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("build clientset: %w", err)
	}
	return clientset, nil
}
