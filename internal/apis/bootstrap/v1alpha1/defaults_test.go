package v1alpha1

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestSetDefaults_FillsEveryZeroField(t *testing.T) {
	c := &NanoKubeConfig{
		Spec: NanoKubeConfigSpec{
			ControlPlane: ControlPlaneSpec{AdvertiseAddress: "10.0.0.1"},
		},
	}
	SetDefaults(c)

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"APIVersion", c.APIVersion, APIVersion},
		{"Kind", c.Kind, Kind},
		{"Mode", c.Spec.ControlPlane.Mode, ControlPlaneModeSingle},
		{"BindPort", c.Spec.ControlPlane.BindPort, DefaultBindPort},
		{"ServiceSubnet", c.Spec.ControlPlane.ServiceSubnet, DefaultServiceSubnet},
		{"PodSubnet", c.Spec.ControlPlane.PodSubnet, DefaultPodSubnet},
		{"ClusterDNS", c.Spec.ControlPlane.ClusterDNS, DefaultClusterDNS},
		{"CRISocket", c.Spec.Runtime.CRISocket, DefaultCRISocket},
		{"CgroupDriver", c.Spec.Runtime.CgroupDriver, CgroupDriverSystemd},
		{"CAValidityDays", c.Spec.Certificates.CAValidityDays, DefaultCAValidityDays},
		{"LeafValidityDays", c.Spec.Certificates.LeafValidityDays, DefaultLeafValidityDays},
	}
	for _, ck := range checks {
		if !reflect.DeepEqual(ck.got, ck.want) {
			t.Errorf("%s = %v, want %v", ck.field, ck.got, ck.want)
		}
	}
}

func TestSetDefaults_PreservesUserValues(t *testing.T) {
	c := &NanoKubeConfig{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: Kind},
		Spec: NanoKubeConfigSpec{
			ControlPlane: ControlPlaneSpec{
				Mode:             ControlPlaneModeSingle,
				AdvertiseAddress: "10.0.0.1",
				BindPort:         7443,
				ServiceSubnet:    "10.100.0.0/16",
				PodSubnet:        "10.200.0.0/16",
				ClusterDNS:       "10.100.0.10",
			},
			Runtime: RuntimeSpec{
				CRISocket:    "unix:///var/run/containerd/containerd.sock",
				CgroupDriver: CgroupDriverCgroupfs,
			},
			Certificates: CertificatesSpec{
				CAValidityDays:   100,
				LeafValidityDays: 50,
			},
		},
	}
	SetDefaults(c)

	if c.Spec.ControlPlane.BindPort != 7443 {
		t.Errorf("user BindPort overwritten: got %d", c.Spec.ControlPlane.BindPort)
	}
	if c.Spec.Runtime.CgroupDriver != CgroupDriverCgroupfs {
		t.Errorf("user CgroupDriver overwritten: got %q", c.Spec.Runtime.CgroupDriver)
	}
	if c.Spec.ControlPlane.ServiceSubnet != "10.100.0.0/16" {
		t.Errorf("user ServiceSubnet overwritten: got %q", c.Spec.ControlPlane.ServiceSubnet)
	}
	if c.Spec.Certificates.CAValidityDays != 100 {
		t.Errorf("user CAValidityDays overwritten: got %d", c.Spec.Certificates.CAValidityDays)
	}
}

// Taints field has a three-state convention: nil => apply default,
// []  => explicit empty (workloads on CP), else => exact list. Cover all
// three so a careless rewrite that conflates nil and empty regresses.
func TestSetDefaults_TaintsTriState(t *testing.T) {
	cases := []struct {
		name string
		in   []corev1.Taint
		want []corev1.Taint
	}{
		{
			name: "nil inherits default control-plane taint",
			in:   nil,
			want: []corev1.Taint{DefaultControlPlaneTaint},
		},
		{
			name: "explicit empty stays empty",
			in:   []corev1.Taint{},
			want: []corev1.Taint{},
		},
		{
			name: "explicit value preserved verbatim",
			in:   []corev1.Taint{{Key: "custom", Effect: corev1.TaintEffectNoExecute}},
			want: []corev1.Taint{{Key: "custom", Effect: corev1.TaintEffectNoExecute}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &NanoKubeConfig{
				Spec: NanoKubeConfigSpec{
					ControlPlane:     ControlPlaneSpec{AdvertiseAddress: "10.0.0.1"},
					NodeRegistration: NodeRegistrationSpec{Taints: tc.in},
				},
			}
			SetDefaults(c)
			if !reflect.DeepEqual(c.Spec.NodeRegistration.Taints, tc.want) {
				t.Errorf("Taints = %v, want %v", c.Spec.NodeRegistration.Taints, tc.want)
			}
		})
	}
}

// The print-defaults output must itself be Validate-clean; operators rely
// on `nanokube config print-defaults > config.yaml` as a starter template.
func TestNewDefault_IsValid(t *testing.T) {
	c := NewDefault()
	if err := Validate(c); err != nil {
		t.Fatalf("NewDefault() must Validate cleanly; got %v", err)
	}
}

func TestNewDefault_AdvertiseAddressIsPlaceholder(t *testing.T) {
	c := NewDefault()
	if c.Spec.ControlPlane.AdvertiseAddress == "" {
		t.Error("NewDefault must populate advertiseAddress so Validate passes")
	}
}
