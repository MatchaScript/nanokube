package kubeadm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/MatchaScript/nanokube/internal/layout"
)

// FinalizeKubeletKubeconfig mirrors kubeadm's kubelet-finalize phase:
// once the kubelet's rotation store (kubelet-client-current.pem) exists,
// kubelet.conf must reference that symlink instead of its embedded
// init-era credential — otherwise an approved rotation does not survive
// a kubelet restart and node auth dies when the embedded cert expires.
// Idempotent; returns true when kubelet.conf was rewritten.
func FinalizeKubeletKubeconfig(l layout.Layout) (bool, error) {
	pem := filepath.Join(l.KubeletDir, "pki", "kubelet-client-current.pem")
	if _, err := os.Stat(pem); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}

	// A missing kubelet.conf is not an error: on a worker the file is
	// written solely by the kubelet's TLS bootstrap, and the kubelet
	// persists the rotation store BEFORE writing kubelet.conf — a crash
	// in that window leaves "pem present, kubelet.conf absent". Treat it
	// like the missing-pem case so the boot proceeds and the kubelet can
	// finish its bootstrap on start.
	kc, err := clientcmd.LoadFromFile(l.KubeletKubeconfig)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load kubelet.conf: %w", err)
	}
	ctx, ok := kc.Contexts[kc.CurrentContext]
	if !ok {
		return false, fmt.Errorf("kubelet.conf: current context %q not found", kc.CurrentContext)
	}
	info, ok := kc.AuthInfos[ctx.AuthInfo]
	if !ok {
		return false, fmt.Errorf("kubelet.conf: auth info %q not found", ctx.AuthInfo)
	}
	if info.ClientCertificate == pem && info.ClientKey == pem &&
		len(info.ClientCertificateData) == 0 && len(info.ClientKeyData) == 0 {
		return false, nil
	}

	info.ClientCertificate = pem
	info.ClientKey = pem
	info.ClientCertificateData = nil
	info.ClientKeyData = nil
	if err := clientcmd.WriteToFile(*kc, l.KubeletKubeconfig); err != nil {
		return false, fmt.Errorf("write kubelet.conf: %w", err)
	}
	return true, nil
}
