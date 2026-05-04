package v1alpha1

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// validCopy returns a fully populated NanoKubeConfig that passes Validate.
// Tests mutate the returned copy and re-check Validate.
func validCopy() *NanoKubeConfig {
	c := NewDefault()
	c.Spec.ControlPlane.AdvertiseAddress = "192.168.10.10"
	return c
}

func TestValidate_AcceptsDefaultConfig(t *testing.T) {
	if err := Validate(validCopy()); err != nil {
		t.Fatalf("Validate(default) = %v; want nil", err)
	}
}

// Table-driven failure cases — each entry mutates one field and asserts
// Validate reports a problem mentioning the expected field path.
func TestValidate_RejectsInvalidFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*NanoKubeConfig)
		want   string
	}{
		{
			name:   "wrong apiVersion",
			mutate: func(c *NanoKubeConfig) { c.APIVersion = "bootstrap.example.com/v1" },
			want:   "apiVersion",
		},
		{
			name:   "wrong kind",
			mutate: func(c *NanoKubeConfig) { c.Kind = "NotNanoKubeConfig" },
			want:   "kind",
		},
		{
			name:   "unsupported control-plane mode",
			mutate: func(c *NanoKubeConfig) { c.Spec.ControlPlane.Mode = "multi" },
			want:   "spec.controlPlane.mode",
		},
		{
			name:   "missing advertiseAddress",
			mutate: func(c *NanoKubeConfig) { c.Spec.ControlPlane.AdvertiseAddress = "" },
			want:   "spec.controlPlane.advertiseAddress is required",
		},
		{
			name:   "non-IP advertiseAddress",
			mutate: func(c *NanoKubeConfig) { c.Spec.ControlPlane.AdvertiseAddress = "not-an-ip" },
			want:   "advertiseAddress",
		},
		{
			name:   "bindPort out of range (zero)",
			mutate: func(c *NanoKubeConfig) { c.Spec.ControlPlane.BindPort = 0 },
			want:   "bindPort",
		},
		{
			name:   "bindPort out of range (negative)",
			mutate: func(c *NanoKubeConfig) { c.Spec.ControlPlane.BindPort = -1 },
			want:   "bindPort",
		},
		{
			name:   "bindPort out of range (>65535)",
			mutate: func(c *NanoKubeConfig) { c.Spec.ControlPlane.BindPort = 70000 },
			want:   "bindPort",
		},
		{
			name:   "invalid service subnet",
			mutate: func(c *NanoKubeConfig) { c.Spec.ControlPlane.ServiceSubnet = "garbage" },
			want:   "serviceSubnet",
		},
		{
			name:   "invalid pod subnet",
			mutate: func(c *NanoKubeConfig) { c.Spec.ControlPlane.PodSubnet = "bad" },
			want:   "podSubnet",
		},
		{
			name:   "invalid clusterDNS",
			mutate: func(c *NanoKubeConfig) { c.Spec.ControlPlane.ClusterDNS = "nope" },
			want:   "clusterDNS",
		},
		{
			name:   "criSocket without unix:// prefix",
			mutate: func(c *NanoKubeConfig) { c.Spec.Runtime.CRISocket = "/var/run/crio/crio.sock" },
			want:   "criSocket",
		},
		{
			name:   "criSocket with http scheme",
			mutate: func(c *NanoKubeConfig) { c.Spec.Runtime.CRISocket = "http://wrong" },
			want:   "criSocket",
		},
		{
			name:   "unknown cgroupDriver",
			mutate: func(c *NanoKubeConfig) { c.Spec.Runtime.CgroupDriver = "mystery" },
			want:   "cgroupDriver",
		},
		{
			name:   "selfSigned=false unsupported",
			mutate: func(c *NanoKubeConfig) { c.Spec.Certificates.SelfSigned = false },
			want:   "selfSigned",
		},
		{
			name:   "caValidityDays non-positive",
			mutate: func(c *NanoKubeConfig) { c.Spec.Certificates.CAValidityDays = 0 },
			want:   "caValidityDays",
		},
		{
			name:   "leafValidityDays non-positive",
			mutate: func(c *NanoKubeConfig) { c.Spec.Certificates.LeafValidityDays = -1 },
			want:   "leafValidityDays",
		},
		{
			name: "taint missing key",
			mutate: func(c *NanoKubeConfig) {
				c.Spec.NodeRegistration.Taints = []corev1.Taint{{Effect: corev1.TaintEffectNoSchedule}}
			},
			want: "taints[0].key",
		},
		{
			name: "taint invalid effect",
			mutate: func(c *NanoKubeConfig) {
				c.Spec.NodeRegistration.Taints = []corev1.Taint{{Key: "k", Effect: "TeleportAway"}}
			},
			want: "taints[0].effect",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validCopy()
			tc.mutate(c)
			err := Validate(c)
			if err == nil {
				t.Fatalf("Validate = nil; want error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// Validate must report every problem it finds, not bail on the first one.
// Operators rely on a single pass surfacing the full list.
func TestValidate_AggregatesMultipleErrors(t *testing.T) {
	c := validCopy()
	c.APIVersion = "wrong"
	c.Spec.ControlPlane.BindPort = -5
	c.Spec.Runtime.CRISocket = "not-unix"
	c.Spec.Certificates.CAValidityDays = 0

	err := Validate(c)
	if err == nil {
		t.Fatal("Validate = nil; want aggregated error")
	}
	msg := err.Error()
	for _, want := range []string{"apiVersion", "bindPort", "criSocket", "caValidityDays"} {
		if !strings.Contains(msg, want) {
			t.Errorf("aggregated error %q missing %q", msg, want)
		}
	}
}

func TestValidate_AcceptsNoSchedulePreferNoScheduleNoExecute(t *testing.T) {
	effects := []corev1.TaintEffect{
		corev1.TaintEffectNoSchedule,
		corev1.TaintEffectPreferNoSchedule,
		corev1.TaintEffectNoExecute,
	}
	for _, eff := range effects {
		c := validCopy()
		c.Spec.NodeRegistration.Taints = []corev1.Taint{{Key: "k", Effect: eff}}
		if err := Validate(c); err != nil {
			t.Errorf("Validate rejected effect %q: %v", eff, err)
		}
	}
}
