package v1alpha1

import corev1 "k8s.io/api/core/v1"

const (
	DefaultBindPort      int32 = 6443
	DefaultServiceSubnet       = "10.96.0.0/12"
	DefaultPodSubnet           = "10.244.0.0/16"
	DefaultClusterDNS          = "10.96.0.10"
	DefaultCRISocket           = "unix:///var/run/crio/crio.sock"
	// DefaultCAValidityDays mirrors kubeadm (10 years). Long because
	// nanokube does not rotate the CA itself; CA rotation is an operator
	// task tied to image rebuilds.
	DefaultCAValidityDays int32 = 3650
	// DefaultLeafValidityDays mirrors kubeadm (1 year). Leaf certificates
	// (apiserver, kubelet client, etc.) are nanokube's responsibility to
	// rotate before expiry — see the cert-rotation TODO below. A multi-
	// year default would mask a missing rotation path on edge devices that
	// rarely reboot.
	DefaultLeafValidityDays int32 = 365

	// ControlPlaneTaintKey is the kubeadm-standard node taint applied to
	// control-plane nodes. Kept in sync with
	// kubeadmconstants.LabelNodeRoleControlPlane.
	ControlPlaneTaintKey = "node-role.kubernetes.io/control-plane"
)

// DefaultControlPlaneTaint is the single-taint default used when the user
// leaves spec.nodeRegistration.taints unset. Matches kubeadm's
// DefaultedStaticInitConfiguration() output.
var DefaultControlPlaneTaint = corev1.Taint{
	Key:    ControlPlaneTaintKey,
	Effect: corev1.TaintEffectNoSchedule,
}

// TODO(certs): nanokube owns leaf-certificate rotation. The current
// init path only mints leaves at install time using LeafValidityDays;
// before any default exceeds a kubelet/apiserver deployment's expected
// uptime we need:
//   1. a renewal phase in `nanokube boot` that re-issues leaves when
//      they cross a configurable threshold (kubeadm uses 30d remaining),
//   2. atomic write + reload signalling for kube-apiserver / kubelet,
//   3. a CA-expiry warning surfaced via WriteLastEvent so operators see
//      it via greenboot/MOTD long before the cluster breaks.
// See reference/kubeadm/cmd/kubeadm/app/phases/certs/renewal/.

// SetDefaults fills zero-valued fields with the project defaults.
// It mutates the argument in place.
func SetDefaults(c *NanoKubeConfig) {
	if c.APIVersion == "" {
		c.APIVersion = APIVersion
	}
	if c.Kind == "" {
		c.Kind = Kind
	}

	cp := &c.Spec.ControlPlane
	if cp.Mode == "" {
		cp.Mode = ControlPlaneModeSingle
	}
	if cp.BindPort == 0 {
		cp.BindPort = DefaultBindPort
	}
	if cp.ServiceSubnet == "" {
		cp.ServiceSubnet = DefaultServiceSubnet
	}
	if cp.PodSubnet == "" {
		cp.PodSubnet = DefaultPodSubnet
	}
	if cp.ClusterDNS == "" {
		cp.ClusterDNS = DefaultClusterDNS
	}

	rt := &c.Spec.Runtime
	if rt.CRISocket == "" {
		rt.CRISocket = DefaultCRISocket
	}
	if rt.CgroupDriver == "" {
		rt.CgroupDriver = CgroupDriverSystemd
	}

	certs := &c.Spec.Certificates
	if certs.CAValidityDays == 0 {
		certs.CAValidityDays = DefaultCAValidityDays
	}
	if certs.LeafValidityDays == 0 {
		certs.LeafValidityDays = DefaultLeafValidityDays
	}

	// Taints: nil => default, [] => explicit "no taints". SetDefaults only
	// substitutes when the user did not set the field at all (nil slice).
	nr := &c.Spec.NodeRegistration
	if nr.Taints == nil {
		nr.Taints = []corev1.Taint{DefaultControlPlaneTaint}
	}
}

// NewDefault returns a NanoKubeConfig with all defaults applied and a
// placeholder advertiseAddress. Used by `config print-defaults`.
//
// The placeholder is RFC 5737 TEST-NET-1 (192.0.2.0/24) — guaranteed not
// to be routable in production, so an operator who forgets to overwrite
// it gets an obvious failure rather than silently binding to 0.0.0.0
// (which Validate rejects as unspecified, breaking apiserver SANs).
func NewDefault() *NanoKubeConfig {
	c := &NanoKubeConfig{
		Metadata: ObjectMeta{Name: "local"},
		Spec: NanoKubeConfigSpec{
			ControlPlane: ControlPlaneSpec{
				AdvertiseAddress: "192.0.2.1",
			},
			Certificates: CertificatesSpec{
				SelfSigned: true,
				ExtraSANs:  []string{"127.0.0.1", "localhost"},
			},
		},
	}
	SetDefaults(c)
	return c
}
