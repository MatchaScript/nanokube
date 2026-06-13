// Package config loads a nanokube configuration file from disk and
// returns it as kubeadm's internal *InitConfiguration. The on-disk
// format is a multi-document YAML stream: one NanoKubeConfig wrapper
// document plus the standard kubeadm InitConfiguration / Cluster
// Configuration / KubeletConfiguration documents that nanokube hands
// straight to kubeadm phases at runtime.
//
// Parsing of the kubeadm portion is delegated to kubeadm's own
// BytesToInitConfiguration helper. That gives nanokube the upstream
// defaulter, the upstream validator, and — critically — kubeadm's
// deprecation warning path for older API versions (klog.Warningf
// emitted by validateSupportedVersion) for free.
package config

import (
	"bytes"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime/schema"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmapiv1 "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/v1beta4"
	"k8s.io/kubernetes/cmd/kubeadm/app/componentconfigs"
	kubeadmutil "k8s.io/kubernetes/cmd/kubeadm/app/util"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/layout"
)

// Loaded is the parsed on-disk configuration. HasJoin reports that the
// file carries a JoinConfiguration document — the CABPK-style marker
// that this node joined an existing cluster (worker, or later an added
// control plane). The role AUTHORITY is last-boot.json; Boot
// cross-checks it against this shape and fails on mismatch.
type Loaded struct {
	Init    *kubeadmapi.InitConfiguration
	HasJoin bool
}

// Load reads the multi-document YAML at path, parses both the
// NanoKubeConfig wrapper and the sibling kubeadm documents, applies
// defaults, validates, and returns the upstream kubeadm internal
// InitConfiguration that downstream packages consume directly.
func Load(path string, l layout.Layout) (*Loaded, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parse(data, path, l)
}

// LoadDefault reads the canonical config file for the given layout.
func LoadDefault(l layout.Layout) (*Loaded, error) {
	return Load(l.ConfigFile, l)
}

func parse(data []byte, source string, l layout.Layout) (*Loaded, error) {
	gvkmap, err := kubeadmutil.SplitConfigDocuments(data)
	if err != nil {
		return nil, fmt.Errorf("split %s: %w", source, err)
	}

	// A JoinConfiguration document marks a joined node (CABPK-style
	// either/or shape). Its contents are validated for well-formedness
	// and then dropped: NodeRegistration is reproduced by kubeadm's
	// dynamic defaulting below, identically to the init path, so no
	// field merging ever happens.
	hasJoin := false
	hasInit := false
	for gvk, doc := range gvkmap {
		if gvk.Group != kubeadmapiv1.SchemeGroupVersion.Group {
			continue
		}
		switch gvk.Kind {
		case "JoinConfiguration":
			if err := yaml.UnmarshalStrict(doc, &kubeadmapiv1.JoinConfiguration{}); err != nil {
				return nil, fmt.Errorf("parse %s JoinConfiguration: %w", source, err)
			}
			hasJoin = true
			delete(gvkmap, gvk)
		case "InitConfiguration":
			hasInit = true
		}
	}
	if hasJoin && hasInit {
		return nil, fmt.Errorf("parse %s: config must carry either an InitConfiguration or a JoinConfiguration document, not both", source)
	}

	// Pull the nanokube wrapper out of the map before handing the rest
	// to kubeadm. Otherwise kubeadm would emit an "Ignored configuration
	// document" klog warning for our wrapper's GVK.
	wrapper, err := extractWrapper(gvkmap, source)
	if err != nil {
		return nil, err
	}
	v1alpha1.SetDefaults(wrapper)

	kubeadmBytes := concatDocs(gvkmap)
	kubeadmCfg, err := kubeadmconfig.BytesToInitConfiguration(kubeadmBytes, false)
	if err != nil {
		return nil, fmt.Errorf("parse kubeadm portion of %s: %w", source, err)
	}

	// kubeadm's defaulter fills CertificatesDir with its own canonical
	// path even when the user left it unset. Restore the empty-string
	// sentinel before validating so Validate only sees values the user
	// explicitly wrote — the empty-string branch is "OK", and we pin
	// l.PKIDir unconditionally afterward.
	if kubeadmCfg.CertificatesDir == kubeadmapiv1.DefaultCertificatesDir {
		kubeadmCfg.CertificatesDir = ""
	}

	if err := v1alpha1.Validate(wrapper, kubeadmCfg, l); err != nil {
		return nil, fmt.Errorf("validate %s: %w", source, err)
	}

	// Pin the on-disk PKI location regardless of whether the user
	// supplied a CertificatesDir. Validate has rejected an explicit
	// non-matching value, so the only mismatch left is the empty default
	// kubeadm filled in ("/etc/kubernetes/pki", which happens to equal
	// paths.PKIDir today) — normalising here keeps every downstream
	// reader on a single canonical path even if paths.PKIDir is
	// retargeted in the future.
	kubeadmCfg.CertificatesDir = l.PKIDir
	return &Loaded{Init: kubeadmCfg, HasJoin: hasJoin}, nil
}

func extractWrapper(gvkmap kubeadmapi.DocumentMap, source string) (*v1alpha1.NanoKubeConfig, error) {
	nkGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupName,
		Version: v1alpha1.Version,
		Kind:    v1alpha1.Kind,
	}
	raw, ok := gvkmap[nkGVK]
	if !ok {
		return nil, fmt.Errorf("parse %s: required document %s (kind=%s) not found", source, v1alpha1.APIVersion, v1alpha1.Kind)
	}
	delete(gvkmap, nkGVK)

	nk := &v1alpha1.NanoKubeConfig{}
	if err := yaml.UnmarshalStrict(raw, nk); err != nil {
		return nil, fmt.Errorf("parse %s NanoKubeConfig: %w", source, err)
	}
	return nk, nil
}

func concatDocs(gvkmap kubeadmapi.DocumentMap) []byte {
	var buf bytes.Buffer
	first := true
	for _, b := range gvkmap {
		if !first {
			buf.WriteString("---\n")
		}
		buf.Write(b)
		if len(b) == 0 || b[len(b)-1] != '\n' {
			buf.WriteByte('\n')
		}
		first = false
	}
	return buf.Bytes()
}

// Marshal serialises a NanoKubeConfig wrapper plus a kubeadm
// InitConfiguration as a multi-document YAML stream. The wrapper is
// emitted via sigs.k8s.io/yaml; the kubeadm portion goes through
// kubeadm's own MarshalInitConfigurationToBytes (which emits Init- and
// ClusterConfiguration as separate documents and handles TypeMeta
// inlining correctly).
//
// Used by `nanokube config print-defaults`. kubeadmCfg may be nil; in
// that case only the wrapper is emitted.
func Marshal(wrapper *v1alpha1.NanoKubeConfig, kubeadmCfg *kubeadmapi.InitConfiguration) ([]byte, error) {
	wrapperBytes, err := yaml.Marshal(wrapper)
	if err != nil {
		return nil, fmt.Errorf("marshal wrapper: %w", err)
	}
	if kubeadmCfg == nil {
		return wrapperBytes, nil
	}
	kubeadmBytes, err := kubeadmconfig.MarshalInitConfigurationToBytes(kubeadmCfg, kubeadmapiv1.SchemeGroupVersion)
	if err != nil {
		return nil, fmt.Errorf("marshal kubeadm portion: %w", err)
	}

	var buf bytes.Buffer
	buf.Write(wrapperBytes)
	if len(wrapperBytes) == 0 || wrapperBytes[len(wrapperBytes)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("---\n")
	buf.Write(kubeadmBytes)
	return buf.Bytes(), nil
}

// MarshalJoin serialises the joined-node config shape: wrapper +
// JoinConfiguration + ClusterConfiguration + the kubelet component
// config, as a multi-document YAML stream. The ClusterConfiguration and
// kubelet config are the node-local CACHE of the cluster's
// kubeadm-config / kubelet-config ConfigMaps, so Boot can re-render
// kubelet files offline.
func MarshalJoin(wrapper *v1alpha1.NanoKubeConfig, joinCfg *kubeadmapi.JoinConfiguration, clusterCfg *kubeadmapi.ClusterConfiguration) ([]byte, error) {
	docs := [][]byte{}

	// Ensure the wrapper carries apiVersion/kind so Load recognises the
	// document on round-trip; yaml.Marshal omits empty TypeMeta fields.
	v1alpha1.SetDefaults(wrapper)
	wrapperBytes, err := yaml.Marshal(wrapper)
	if err != nil {
		return nil, fmt.Errorf("marshal wrapper: %w", err)
	}
	docs = append(docs, wrapperBytes)

	joinBytes, err := kubeadmconfig.MarshalKubeadmConfigObject(joinCfg, kubeadmapiv1.SchemeGroupVersion)
	if err != nil {
		return nil, fmt.Errorf("marshal JoinConfiguration: %w", err)
	}
	docs = append(docs, joinBytes)

	clusterBytes, err := kubeadmconfig.MarshalKubeadmConfigObject(clusterCfg, kubeadmapiv1.SchemeGroupVersion)
	if err != nil {
		return nil, fmt.Errorf("marshal ClusterConfiguration: %w", err)
	}
	docs = append(docs, clusterBytes)

	if kc, ok := clusterCfg.ComponentConfigs[componentconfigs.KubeletGroup]; ok {
		kcBytes, err := kc.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshal kubelet component config: %w", err)
		}
		docs = append(docs, kcBytes)
	}

	var buf bytes.Buffer
	for i, d := range docs {
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(d)
		if len(d) == 0 || d[len(d)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}
