package kubeadm

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"
)

func TestEnsureJoinPrereqs_CreatesJoinObjects(t *testing.T) {
	client := fake.NewSimpleClientset()
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("defaulted config: %v", err)
	}
	adminCfg := clientcmdapi.NewConfig()
	adminCfg.Clusters["c"] = &clientcmdapi.Cluster{
		Server:                   "https://cp.example.test:6443",
		CertificateAuthorityData: []byte("fake-ca"),
	}
	adminCfg.Contexts["c"] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "a"}
	adminCfg.AuthInfos["a"] = &clientcmdapi.AuthInfo{}
	adminCfg.CurrentContext = "c"

	if err := EnsureJoinPrereqs(cfg, client, adminCfg); err != nil {
		t.Fatalf("EnsureJoinPrereqs: %v", err)
	}

	for _, cm := range []struct{ ns, name string }{
		{"kube-system", "kubeadm-config"},
		{"kube-system", "kubelet-config"},
		{"kube-public", "cluster-info"},
	} {
		if _, err := client.CoreV1().ConfigMaps(cm.ns).Get(context.Background(), cm.name, metav1.GetOptions{}); err != nil {
			t.Errorf("ConfigMap %s/%s: %v", cm.ns, cm.name, err)
		}
	}
	for _, crb := range []string{
		"kubeadm:kubelet-bootstrap",
		"kubeadm:node-autoapprove-bootstrap",
		"kubeadm:node-autoapprove-certificate-rotation",
	} {
		if _, err := client.RbacV1().ClusterRoleBindings().Get(context.Background(), crb, metav1.GetOptions{}); err != nil {
			t.Errorf("ClusterRoleBinding %s: %v", crb, err)
		}
	}
}
