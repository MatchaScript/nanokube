// Package addnode implements `nanokube add-node`: the discrete,
// node-local worker join modelled on kubeadm join's discovery +
// TLS-bootstrap flow (and microshift add-node's command shape). It
// prepares everything kubelet and Boot need, then hands control to the
// regular `nanokube boot` via a service restart — Boot itself never
// detects joins.
package addnode

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmapiv1 "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/v1beta4"
	"k8s.io/kubernetes/cmd/kubeadm/app/discovery"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/backup"
	"github.com/MatchaScript/nanokube/internal/config"
	"github.com/MatchaScript/nanokube/internal/hosts"
	"github.com/MatchaScript/nanokube/internal/layout"
	"github.com/MatchaScript/nanokube/internal/ostree"
	"github.com/MatchaScript/nanokube/internal/state"
)

// Options are the operator-supplied join credentials, produced by
// `nanokube token create` on a control-plane node.
type Options struct {
	Server       string   // reachable apiserver, host:port or https://host:port
	Token        string   // bootstrap token (id.secret)
	CACertHashes []string // sha256:... pins; required (no insecure skip)
}

// normalizeServer accepts "host:port" or "https://host:port" and
// returns (url, host:port, host). The host:port form is what kubeadm's
// BootstrapTokenDiscovery.APIServerEndpoint expects (no scheme).
func normalizeServer(s string) (fullURL, hostPort, host string, err error) {
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", "", "", fmt.Errorf("invalid --server %q: want https://host:port", s)
	}
	h, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid --server %q: missing port", s)
	}
	return "https://" + u.Host, u.Host, h, nil
}

func Run(ctx context.Context, opts Options, l layout.Layout, selfVersion string, out io.Writer) error {
	logf := func(format string, a ...any) { fmt.Fprintf(out, "[add-node] "+format+"\n", a...) }

	// Idempotency: a node that completed TLS bootstrap is joined.
	if _, err := os.Stat(l.KubeletKubeconfig); err == nil {
		logf("kubelet.conf already present — node already joined; nothing to do")
		return nil
	}
	if exists, err := state.Exists(l); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("node carries nanokube state from a previous cluster; run `nanokube reset --yes` first")
	}
	if len(opts.CACertHashes) == 0 {
		return fmt.Errorf("--ca-cert-hash is required (printed by `nanokube token create`)")
	}

	serverURL, hostPort, serverHost, err := normalizeServer(opts.Server)
	if err != nil {
		return err
	}

	// kubeadm token discovery: fetch kube-public/cluster-info
	// anonymously, verify its JWS signature with the token, pin the CA
	// against --ca-cert-hash. The result is the TLS-bootstrap kubeconfig.
	//
	// The internal JoinConfiguration MUST come out of kubeadm's own
	// defaulting pipeline: discovery.For dereferences
	// cfg.Timeouts.Discovery (allocated by v1beta4 SetDefaults_Timeouts),
	// so a hand-built internal struct nil-panics. DefaultedJoinConfiguration
	// also runs SetJoinDynamicDefaults (hostname, CRI detect) and
	// ValidateJoinConfiguration.
	versionedJoin := &kubeadmapiv1.JoinConfiguration{
		Discovery: kubeadmapiv1.Discovery{
			BootstrapToken: &kubeadmapiv1.BootstrapTokenDiscovery{
				Token:             opts.Token,
				APIServerEndpoint: hostPort,
				CACertHashes:      opts.CACertHashes,
			},
			TLSBootstrapToken: opts.Token,
		},
	}
	joinCfg, err := kubeadmconfig.DefaultedJoinConfiguration(versionedJoin, kubeadmconfig.LoadOrDefaultConfigurationOptions{})
	if err != nil {
		return fmt.Errorf("default join configuration: %w", err)
	}
	logf("discovering cluster via %s", hostPort)
	tlsBootstrapCfg, err := discovery.For(nil, joinCfg)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}

	// Persist the cluster CA for kubelet's serving-side client auth and
	// for `nanokube token create` parity on this node later.
	cluster := tlsBootstrapCfg.Clusters[tlsBootstrapCfg.Contexts[tlsBootstrapCfg.CurrentContext].Cluster]
	if err := os.MkdirAll(l.PKIDir, 0o755); err != nil {
		return err
	}
	caPath := filepath.Join(l.PKIDir, "ca.crt")
	if err := os.WriteFile(caPath, cluster.CertificateAuthorityData, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", caPath, err)
	}

	client, err := clientFromKubeconfig(tlsBootstrapCfg)
	if err != nil {
		return err
	}

	// The cluster's kubeadm-config / kubelet-config ConfigMaps are the
	// config authority for joined nodes; cache them locally so Boot can
	// re-render offline on every reboot.
	initCfg, err := kubeadmconfig.FetchInitConfigurationFromCluster(client, nil, "add-node", false, false, true)
	if err != nil {
		return fmt.Errorf("fetch cluster configuration: %w", err)
	}

	// Tier-1 stable-name backing: pin controlPlaneEndpoint to the
	// known-reachable control-plane IP when it has no real DNS here.
	if err := hosts.EnsureEntry(initCfg.ControlPlaneEndpoint, serverHost, logf); err != nil {
		return err
	}

	persistJoin := joinCfg.DeepCopy()
	// The stored document must not carry the short-lived token; record
	// file discovery against the credential kubelet will own.
	persistJoin.Discovery = kubeadmapi.Discovery{
		File: &kubeadmapi.FileDiscovery{KubeConfigPath: l.KubeletKubeconfig},
	}
	data, err := config.MarshalJoin(v1alpha1.NewDefault(), persistJoin, &initCfg.ClusterConfiguration)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(l.ConfigDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(l.ConfigFile, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", l.ConfigFile, err)
	}
	logf("wrote %s (JoinConfiguration + cached ClusterConfiguration)", l.ConfigFile)

	if err := writeJoinState(l, selfVersion, serverURL); err != nil {
		return err
	}

	if err := clientcmd.WriteToFile(*tlsBootstrapCfg, l.BootstrapKubeletKubeconfig); err != nil {
		return fmt.Errorf("write bootstrap-kubelet.conf: %w", err)
	}

	// Hand off to the regular boot path. nanokube.service is Type=notify
	// and signals READY=1 only after the worker's three-stage wait
	// passes (kubelet healthz -> TLS bootstrap -> own Node Ready), so a
	// successful restart IS join confirmation.
	logf("starting nanokube.service (blocks until the node is Ready)")
	cmd := exec.CommandContext(ctx, "systemctl", "restart", "nanokube.service")
	if cmdOut, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart nanokube: %v: %s\ninspect with: journalctl -u nanokube.service", err, cmdOut)
	}

	// getNodeRegistration was false in FetchInitConfigurationFromCluster,
	// so initCfg.NodeRegistration.Name is unset; the local node name comes
	// from joinCfg, which DefaultedJoinConfiguration filled from hostname.
	logf("join complete (node=%s)", joinCfg.NodeRegistration.Name)
	logf("next step: `systemctl enable nanokube.service`")
	return nil
}

// writeJoinState records the node's durable identity: role=worker and
// the nanokube-internal apiServerURLs bypass list (NOT kubelet's
// endpoint — kubelet.conf points at controlPlaneEndpoint).
func writeJoinState(l layout.Layout, selfVersion, serverURL string) error {
	bootID, err := backup.BootID()
	if err != nil {
		return err
	}
	deploymentID := ""
	if isOSTree, err := ostree.IsOSTree(); err != nil {
		return err
	} else if isOSTree {
		if deploymentID, err = ostree.BootedDeploymentID(); err != nil {
			return err
		}
	}
	if err := state.WriteLastBoot(l, state.LastBoot{
		Version:       selfVersion,
		DeploymentID:  deploymentID,
		BootID:        bootID,
		Role:          state.RoleWorker,
		APIServerURLs: []string{serverURL},
	}); err != nil {
		return err
	}
	_ = state.WriteLastEvent(l, fmt.Sprintf("joined as worker at %s", selfVersion))
	return nil
}

func clientFromKubeconfig(c *clientcmdapi.Config) (kubernetes.Interface, error) {
	restCfg, err := clientcmd.NewDefaultClientConfig(*c, nil).ClientConfig()
	if err != nil {
		return nil, err
	}
	restCfg.Timeout = 10 * time.Second
	return kubernetes.NewForConfig(restCfg)
}
