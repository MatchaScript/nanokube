package kubeadm

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
)

// BuildInitConfiguration must forward spec.nodeRegistration.taints
// verbatim. kubeadm's own DefaultedStaticInitConfiguration would otherwise
// inject the canonical CP taint, clobbering an operator's explicit
// "empty slice = no taints" request.
func TestBuildInitConfiguration_TaintsRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		taints []corev1.Taint
	}{
		{
			name:   "default control-plane taint",
			taints: []corev1.Taint{v1alpha1.DefaultControlPlaneTaint},
		},
		{
			name:   "explicit empty slice",
			taints: []corev1.Taint{},
		},
		{
			name: "custom multi-taint",
			taints: []corev1.Taint{
				{Key: "workload", Effect: corev1.TaintEffectNoSchedule},
				{Key: "evict-me", Effect: corev1.TaintEffectNoExecute},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.Spec.NodeRegistration.Taints = tc.taints

			kc, err := BuildInitConfiguration(cfg, testLayout(t), "cp")
			if err != nil {
				t.Fatalf("BuildInitConfiguration: %v", err)
			}
			if !reflect.DeepEqual(kc.NodeRegistration.Taints, tc.taints) {
				t.Errorf("NodeRegistration.Taints = %v; want %v", kc.NodeRegistration.Taints, tc.taints)
			}
		})
	}
}

func TestBuildInitConfiguration_ForwardsCRISocket(t *testing.T) {
	cfg := testConfig()
	cfg.Spec.Runtime.CRISocket = "unix:///var/run/containerd/containerd.sock"

	kc, err := BuildInitConfiguration(cfg, testLayout(t), "cp")
	if err != nil {
		t.Fatal(err)
	}
	if kc.NodeRegistration.CRISocket != cfg.Spec.Runtime.CRISocket {
		t.Errorf("CRISocket = %q; want %q", kc.NodeRegistration.CRISocket, cfg.Spec.Runtime.CRISocket)
	}
}

// Extra SANs must land on both etcd (server+peer) and the apiserver.
// This is what distinguishes a working single-node install from one that
// breaks on `kubectl --server=https://127.0.0.1:6443` due to SAN mismatch.
func TestBuildInitConfiguration_ExtraSANsPopulateEtcdAndAPIServer(t *testing.T) {
	cfg := testConfig()
	cfg.Spec.Certificates.ExtraSANs = []string{"127.0.0.1", "nanokube.local", "10.0.0.5"}

	kc, err := BuildInitConfiguration(cfg, testLayout(t), "cp")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kc.APIServer.CertSANs, cfg.Spec.Certificates.ExtraSANs) {
		t.Errorf("APIServer.CertSANs = %v; want %v", kc.APIServer.CertSANs, cfg.Spec.Certificates.ExtraSANs)
	}
	if kc.Etcd.Local == nil {
		t.Fatalf("Etcd.Local nil; expected local etcd")
	}
	if !reflect.DeepEqual(kc.Etcd.Local.ServerCertSANs, cfg.Spec.Certificates.ExtraSANs) {
		t.Errorf("Etcd.Local.ServerCertSANs = %v; want %v", kc.Etcd.Local.ServerCertSANs, cfg.Spec.Certificates.ExtraSANs)
	}
	if !reflect.DeepEqual(kc.Etcd.Local.PeerCertSANs, cfg.Spec.Certificates.ExtraSANs) {
		t.Errorf("Etcd.Local.PeerCertSANs = %v; want %v", kc.Etcd.Local.PeerCertSANs, cfg.Spec.Certificates.ExtraSANs)
	}
}

func TestBuildInitConfiguration_PodAndServiceSubnetsForwarded(t *testing.T) {
	cfg := testConfig()
	cfg.Spec.ControlPlane.PodSubnet = "172.16.0.0/16"
	cfg.Spec.ControlPlane.ServiceSubnet = "172.17.0.0/16"
	cfg.Spec.ControlPlane.ClusterDNS = "172.17.0.10"

	kc, err := BuildInitConfiguration(cfg, testLayout(t), "cp")
	if err != nil {
		t.Fatal(err)
	}
	if kc.Networking.PodSubnet != cfg.Spec.ControlPlane.PodSubnet {
		t.Errorf("PodSubnet = %q; want %q", kc.Networking.PodSubnet, cfg.Spec.ControlPlane.PodSubnet)
	}
	if kc.Networking.ServiceSubnet != cfg.Spec.ControlPlane.ServiceSubnet {
		t.Errorf("ServiceSubnet = %q; want %q", kc.Networking.ServiceSubnet, cfg.Spec.ControlPlane.ServiceSubnet)
	}
}

// DefaultLayout must point at the canonical kubeadm paths; operators and
// packaging rely on these locations.
func TestDefaultLayout_PointsAtKubeadmPaths(t *testing.T) {
	l := DefaultLayout()
	if l.PKIDir == "" || l.KubeconfigDir == "" || l.ManifestsDir == "" || l.KubeletDir == "" {
		t.Fatalf("DefaultLayout has empty fields: %+v", l)
	}
	// Sanity — relocations in testutil.UseTempPaths are opt-in and must
	// not apply here (DefaultLayout is read once at call time).
	if l.PKIDir[0] != '/' {
		t.Errorf("PKIDir not absolute: %q", l.PKIDir)
	}
}
