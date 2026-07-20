package render

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
)

// manifestVariant is one entry in the ControlPlaneManifests golden/parity
// matrix: a name (testdata subdirectory, parity subtest name) and a
// mutation applied to a freshly defaulted InitConfiguration to produce
// that variant. Shared by golden_test.go (always run) and
// parity_test.go (kubeadm_parity build tag only) so both exercise
// exactly the same inputs.
type manifestVariant struct {
	name   string
	mutate func(cfg *kubeadmapi.InitConfiguration)
}

func manifestVariants() []manifestVariant {
	dirType := corev1.HostPathDirectoryOrCreate
	return []manifestVariant{
		{"default", func(cfg *kubeadmapi.InitConfiguration) {}},
		{"apiserver-extra-args", func(cfg *kubeadmapi.InitConfiguration) {
			// audit-log-path exercises plain passthrough; authorization-mode
			// exercises the override-wins-verbatim behavior documented on
			// apiServerCommand.
			cfg.APIServer.ExtraArgs = []kubeadmapi.Arg{
				{Name: "audit-log-path", Value: "-"},
				{Name: "authorization-mode", Value: "Node,RBAC,Webhook"},
			}
		}},
		{"cert-sans", func(cfg *kubeadmapi.InitConfiguration) {
			// CertSANs feed apiserver certificate generation (internal/certs),
			// not the static pod manifests. Included because the task
			// explicitly names it as a variation to cover; expected to
			// produce byte-identical manifests to "default".
			cfg.APIServer.CertSANs = []string{"cp.example.invalid", "10.0.0.5"}
		}},
		{"non-default-cidrs", func(cfg *kubeadmapi.InitConfiguration) {
			cfg.Networking.PodSubnet = "10.244.0.0/16"
			cfg.Networking.ServiceSubnet = "10.98.0.0/12"
		}},
		{"extra-volumes-and-envs", func(cfg *kubeadmapi.InitConfiguration) {
			// Same-name ExtraVolumes entry must override the default
			// "ca-certs" mount rather than add a second one.
			cfg.APIServer.ExtraVolumes = []kubeadmapi.HostPathMount{
				{Name: "ca-certs", HostPath: "/opt/custom-ca", MountPath: "/opt/custom-ca", ReadOnly: true, PathType: dirType},
			}
			cfg.APIServer.ExtraEnvs = []kubeadmapi.EnvVar{{EnvVar: corev1.EnvVar{Name: "FOO", Value: "bar"}}}
		}},
		{"controller-manager-flexvolume-dir", func(cfg *kubeadmapi.InitConfiguration) {
			// Non-default flex-volume-plugin-dir must replace the
			// flexvolume hostPath mount's path.
			cfg.ControllerManager.ExtraArgs = []kubeadmapi.Arg{
				{Name: "flex-volume-plugin-dir", Value: "/opt/custom-flexvolume"},
			}
		}},
		{"etcd-extra-args", func(cfg *kubeadmapi.InitConfiguration) {
			// advertise-client-urls must replace the apiserver's
			// --etcd-servers value; listen-metrics-urls (non-default
			// scheme+port) must be observable in the etcd probe
			// derivation (etcdProbeEndpoint's URL parse).
			cfg.Etcd.Local.ExtraArgs = []kubeadmapi.Arg{
				{Name: "advertise-client-urls", Value: "https://192.0.2.10:2379"},
				{Name: "listen-metrics-urls", Value: "https://127.0.0.1:2381"},
			}
		}},
		{"apiserver-invalid-authz-mode", func(cfg *kubeadmapi.InitConfiguration) {
			// A user-supplied authorization-mode containing an actually
			// invalid mode name must still be passed through wholesale
			// and unfiltered (apiServerCommand's doc comment).
			cfg.APIServer.ExtraArgs = []kubeadmapi.Arg{
				{Name: "authorization-mode", Value: "Node,RBAC,Bogus"},
			}
		}},
	}
}

// variantConfig builds the defaulted InitConfiguration for v, sharing the
// same AdvertiseAddress fixture the pre-existing ControlPlaneManifests
// tests use.
func variantConfig(t *testing.T, v manifestVariant) *kubeadmapi.InitConfiguration {
	t.Helper()
	cfg := defaultedInit(t)
	cfg.LocalAPIEndpoint.AdvertiseAddress = "192.0.2.10"
	v.mutate(cfg)
	return cfg
}
