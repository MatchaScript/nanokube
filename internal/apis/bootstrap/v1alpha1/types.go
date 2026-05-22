// Package v1alpha1 defines the NanoKubeConfig wrapper document. nanokube
// configuration files are multi-document YAML streams modelled on
// kubeadm's `--config`: a single NanoKubeConfig document plus the kubeadm
// InitConfiguration / ClusterConfiguration / KubeletConfiguration
// documents that nanokube hands to kubeadm at runtime.
//
// The wrapper carries nanokube-only metadata (apiVersion / kind for the
// loader to identify the file as ours) and a reserved Spec for future
// nanokube-only knobs. Cluster, node-registration, and kubelet settings
// live exclusively in the kubeadm documents — nanokube does not
// re-expose them.
//
// This package is intentionally small: it owns the wrapper type and the
// load-time gate (apiVersion / kind / JoinConfiguration absent /
// CertificatesDir at the pinned path). The kubeadm portion of the
// configuration is parsed by config.Load via kubeadm's own
// BytesToInitConfiguration and returned as the upstream internal type
// *kubeadmapi.InitConfiguration; downstream code talks to that type
// directly rather than to a nanokube-shaped re-packaging.
package v1alpha1

const (
	GroupName  = "bootstrap.nanokube.io"
	Version    = "v1alpha1"
	APIVersion = GroupName + "/" + Version
	Kind       = "NanoKubeConfig"
)

type TypeMeta struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

type ObjectMeta struct {
	Name string `json:"name,omitempty"`
}

// NanoKubeConfig is the wrapper document that identifies a YAML stream as
// a nanokube configuration file.
type NanoKubeConfig struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta         `json:"metadata,omitempty"`
	Spec     NanoKubeConfigSpec `json:"spec,omitempty"`
}

// NanoKubeConfigSpec is reserved for nanokube-only settings that have no
// kubeadm equivalent (e.g. a node-local etcd snapshot policy). Empty
// today — every current knob lives in the sibling kubeadm documents.
type NanoKubeConfigSpec struct{}
