package v1alpha1

import (
	"errors"
	"fmt"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// TODO(validate): add cross-field checks that kubeadm enforces but
// nanokube currently does not:
//   - serviceSubnet and podSubnet must not overlap (kubeadm
//     ValidateNetworking) — overlap silently routes pod traffic into
//     the service CIDR.
//   - clusterDNS must lie inside serviceSubnet, otherwise CoreDNS
//     allocates a Service IP that nothing routes to.
// These are pure-data checks that belong here rather than in kubeadm
// glue code.

// Validate returns an aggregated error describing every problem found in c,
// or nil if the configuration is acceptable. Defaults should be applied
// before calling Validate.
func Validate(c *NanoKubeConfig) error {
	var errs []error

	if c.APIVersion != APIVersion {
		errs = append(errs, fmt.Errorf("apiVersion must be %q, got %q", APIVersion, c.APIVersion))
	}
	if c.Kind != Kind {
		errs = append(errs, fmt.Errorf("kind must be %q, got %q", Kind, c.Kind))
	}

	cp := c.Spec.ControlPlane
	if cp.Mode != ControlPlaneModeSingle {
		errs = append(errs, fmt.Errorf("spec.controlPlane.mode must be %q in v0", ControlPlaneModeSingle))
	}
	if cp.AdvertiseAddress == "" {
		errs = append(errs, errors.New("spec.controlPlane.advertiseAddress is required"))
	} else if ip := net.ParseIP(cp.AdvertiseAddress); ip == nil {
		errs = append(errs, fmt.Errorf("spec.controlPlane.advertiseAddress %q is not a valid IP", cp.AdvertiseAddress))
	} else if ip.IsUnspecified() {
		// 0.0.0.0 / :: parse as valid IPs but break apiserver cert SANs
		// and the in-cluster kubernetes.default URL. kubeadm rejects
		// these for the same reason.
		errs = append(errs, fmt.Errorf("spec.controlPlane.advertiseAddress %q is unspecified; use a routable IP", cp.AdvertiseAddress))
	}
	if cp.BindPort < 1 || cp.BindPort > 65535 {
		errs = append(errs, fmt.Errorf("spec.controlPlane.bindPort %d out of range", cp.BindPort))
	}
	if _, _, err := net.ParseCIDR(cp.ServiceSubnet); err != nil {
		errs = append(errs, fmt.Errorf("spec.controlPlane.serviceSubnet: %w", err))
	}
	if _, _, err := net.ParseCIDR(cp.PodSubnet); err != nil {
		errs = append(errs, fmt.Errorf("spec.controlPlane.podSubnet: %w", err))
	}
	if ip := net.ParseIP(cp.ClusterDNS); ip == nil {
		errs = append(errs, fmt.Errorf("spec.controlPlane.clusterDNS %q is not a valid IP", cp.ClusterDNS))
	}

	rt := c.Spec.Runtime
	if !strings.HasPrefix(rt.CRISocket, "unix://") {
		errs = append(errs, fmt.Errorf("spec.runtime.criSocket must start with unix://, got %q", rt.CRISocket))
	}
	switch rt.CgroupDriver {
	case CgroupDriverSystemd, CgroupDriverCgroupfs:
	default:
		errs = append(errs, fmt.Errorf("spec.runtime.cgroupDriver must be systemd or cgroupfs, got %q", rt.CgroupDriver))
	}

	certs := c.Spec.Certificates
	if !certs.SelfSigned {
		errs = append(errs, errors.New("spec.certificates.selfSigned=false is not supported in v0"))
	}
	if certs.CAValidityDays <= 0 {
		errs = append(errs, errors.New("spec.certificates.caValidityDays must be > 0"))
	}
	if certs.LeafValidityDays <= 0 {
		errs = append(errs, errors.New("spec.certificates.leafValidityDays must be > 0"))
	}

	for i, t := range c.Spec.NodeRegistration.Taints {
		if t.Key == "" {
			errs = append(errs, fmt.Errorf("spec.nodeRegistration.taints[%d].key is required", i))
		}
		switch t.Effect {
		case corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
		default:
			errs = append(errs, fmt.Errorf("spec.nodeRegistration.taints[%d].effect must be one of NoSchedule/PreferNoSchedule/NoExecute, got %q",
				i, t.Effect))
		}
	}

	return errors.Join(errs...)
}
