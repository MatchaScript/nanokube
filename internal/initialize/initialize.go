// Package initialize implements `nanokube init`: the one-time node
// initialisation that mirrors `kubeadm init`'s scope.
//
// Run renders kubeadm artefacts to /etc/kubernetes, starts kubelet,
// waits for the apiserver, seeds the kubeadm:cluster-admins
// ClusterRoleBinding using a just-in-time super-admin.conf
// (system:masters-bound), removes super-admin.conf so the break-glass
// cred does not linger on a long-lived node, marks the control-plane
// node, and applies addons. On success the cluster is healthy and the
// operator's next step is `systemctl enable nanokube.service` to put
// future reboots under supervisor control. lifecycle.Boot handles every
// reboot from then on as a pure reconcile.
//
// Recovery: a partial Run (e.g. /readyz never came up) leaves the node
// in a state state.Exists() detects, so a retry surfaces a clear
// "already exists; run reset" error. The operator-recovery path is
// uniform: `nanokube reset --yes` then `nanokube init`.
package initialize

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/markcontrolplane"

	"github.com/MatchaScript/nanokube/internal/backup"
	"github.com/MatchaScript/nanokube/internal/certs"
	"github.com/MatchaScript/nanokube/internal/healthcheck"
	"github.com/MatchaScript/nanokube/internal/hosts"
	"github.com/MatchaScript/nanokube/internal/kubeadm"
	"github.com/MatchaScript/nanokube/internal/kubeclient"
	"github.com/MatchaScript/nanokube/internal/layout"
	"github.com/MatchaScript/nanokube/internal/ostree"
	"github.com/MatchaScript/nanokube/internal/preflight"
	"github.com/MatchaScript/nanokube/internal/state"
)

// Run executes the full one-time init. out receives human-readable
// progress logs (operator's terminal during `nanokube init`). Returns
// nil only if the cluster is verified healthy at function exit.
//
// cfg is the kubeadm InitConfiguration parsed by config.Load; nanokube
// does not add a configuration layer on top. NodeRegistration.Name has
// already been filled in by kubeadm's SetNodeRegistrationDynamicDefaults
// from the system hostname, so a separate nodeName argument is no
// longer threaded through the call graph.
func Run(ctx context.Context, cfg *kubeadmapi.InitConfiguration, l layout.Layout, selfVersion string, out io.Writer) error {
	logf := func(format string, a ...any) { fmt.Fprintf(out, "[init] "+format+"\n", a...) }
	nodeName := cfg.NodeRegistration.Name

	isOSTree, err := ostree.IsOSTree()
	if err != nil {
		return fmt.Errorf("detect ostree: %w", err)
	}

	checks := []preflight.Preflighter{
		preflight.FSWritable{Dirs: []string{l.NanoKubeVarDir, l.KubernetesDir}},
	}
	if isOSTree {
		checks = append(checks, backup.SpacePreflighter{Layout: l})
	}
	if err := preflight.Run(ctx, checks...); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	if err := hosts.EnsureEntry(cfg.ControlPlaneEndpoint, cfg.LocalAPIEndpoint.AdvertiseAddress, logf); err != nil {
		return fmt.Errorf("ensure controlPlaneEndpoint resolvable: %w", err)
	}

	if err := certs.Init(cfg, l); err != nil {
		return fmt.Errorf("certs init: %w", err)
	}
	logf("provisioned PKI under %s", l.PKIDir)

	if err := kubeadm.Ensure(cfg, l); err != nil {
		return fmt.Errorf("ensure: %w", err)
	}
	logf("rendered static pod manifests and kubelet config")

	// super-admin.conf is written just-in-time so initAdminRBAC can
	// authenticate as system:masters; removeSuperAdminKubeconfig deletes
	// it again before this function returns. Ensure deliberately does not
	// produce super-admin.conf so reconcile boots cannot regenerate it.
	if err := kubeadm.WriteSuperAdminKubeconfig(cfg, l); err != nil {
		return err
	}

	if err := startKubelet(ctx, logf); err != nil {
		return err
	}

	if err := waitReadyz(ctx, l, logf); err != nil {
		return err
	}

	client, err := initAdminRBAC(l)
	if err != nil {
		return err
	}
	logf("seeded kubeadm:cluster-admins ClusterRoleBinding")

	if err := removeSuperAdminKubeconfig(l); err != nil {
		return err
	}
	logf("removed super-admin.conf (regenerate via `nanokube kubeconfig super-admin` if needed)")

	if err := waitControlPlane(ctx, client, nodeName, logf); err != nil {
		return err
	}

	if err := markcontrolplane.MarkControlPlane(client, nodeName, cfg.NodeRegistration.Taints); err != nil {
		return fmt.Errorf("mark control-plane: %w", err)
	}
	logf("marked control-plane node")

	adminCfg, err := clientcmd.LoadFromFile(l.AdminKubeconfig)
	if err != nil {
		return fmt.Errorf("load admin kubeconfig: %w", err)
	}
	if err := kubeadm.EnsureJoinPrereqs(cfg, client, adminCfg); err != nil {
		return fmt.Errorf("join prereqs: %w", err)
	}
	logf("ensured join prerequisites (kubeadm-config, cluster-info, TLS-bootstrap RBAC)")

	if err := kubeadm.EnsureAddons(cfg, client, out); err != nil {
		return fmt.Errorf("addons: %w", err)
	}

	if err := finalizeKubeletConf(ctx, l, logf); err != nil {
		return err
	}

	if err := writeFirstBootState(l, selfVersion, isOSTree); err != nil {
		return err
	}

	logf("init complete (node=%s, version=%s)", nodeName, selfVersion)
	logf("next step: `systemctl enable nanokube.service`")
	return nil
}

// writeFirstBootState records the just-completed init so the next
// `lifecycle.Boot` invocation sees it as the previous-boot baseline (for
// upgrade detection and backup naming).
func writeFirstBootState(l layout.Layout, selfVersion string, isOSTree bool) error {
	bootID, err := backup.BootID()
	if err != nil {
		return err
	}
	deploymentID := ""
	if isOSTree {
		deploymentID, err = ostree.BootedDeploymentID()
		if err != nil {
			return fmt.Errorf("booted deployment id: %w", err)
		}
	}
	if err := state.WriteLastBoot(l, state.LastBoot{
		Version:      selfVersion,
		DeploymentID: deploymentID,
		BootID:       bootID,
		Role:         state.RoleControlPlane,
	}); err != nil {
		return err
	}
	_ = state.WriteLastEvent(l, fmt.Sprintf("initialised at %s", selfVersion))
	return nil
}

// startKubelet asks systemd to start kubelet.service without blocking
// on its readiness. Readiness is verified separately via /readyz.
// Duplicates the equivalent helper in lifecycle/boot.go: init and
// reconcile share these waits but the packages are deliberately
// independent so neither can import the other.
func startKubelet(ctx context.Context, logf func(string, ...any)) error {
	cmd := exec.CommandContext(ctx, "systemctl", "start", "--no-block", "kubelet.service")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl start kubelet: %v: %s", err, out)
	}
	logf("kubelet.service queued for start")
	return nil
}

// readyzTimeout bounds how long init waits for apiserver /readyz after
// asking systemd to start kubelet for the first time.
const readyzTimeout = 3 * time.Minute

func waitReadyz(ctx context.Context, l layout.Layout, logf func(string, ...any)) error {
	logf("waiting for apiserver /readyz (timeout=%s)", readyzTimeout)
	client, err := kubeclient.LoadAdmin(l.AdminKubeconfig)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, readyzTimeout)
	defer cancel()
	if err := healthcheck.WaitForAPIServer(cctx, client); err != nil {
		return err
	}
	logf("apiserver ready")
	return nil
}

// controlPlaneTimeout bounds how long init waits for node Ready + the
// three control-plane static pods Ready once /readyz responded.
const controlPlaneTimeout = 3 * time.Minute

func waitControlPlane(ctx context.Context, client kubernetes.Interface, nodeName string, logf func(string, ...any)) error {
	logf("waiting for node + control-plane static pods Ready (timeout=%s)", controlPlaneTimeout)
	cctx, cancel := context.WithTimeout(ctx, controlPlaneTimeout)
	defer cancel()
	if err := healthcheck.WaitForControlPlane(cctx, client, nodeName); err != nil {
		return err
	}
	logf("control plane ready")
	return nil
}

// finalizeKubeletConf waits briefly for the kubelet's rotation store to
// appear (the kubelet files its first rotation CSR right after start;
// the autoapprove CRBs from EnsureJoinPrereqs let the KCM approve it
// within seconds) and then repoints kubelet.conf at the rotating
// credential, restarting kubelet to apply — same as kubeadm init's
// kubelet-finalize phase. A timeout is not fatal: every subsequent
// `nanokube boot` retries via the same FinalizeKubeletKubeconfig call.
func finalizeKubeletConf(ctx context.Context, l layout.Layout, logf func(string, ...any)) error {
	pem := filepath.Join(l.KubeletDir, "pki", "kubelet-client-current.pem")
	wctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	err := wait.PollUntilContextCancel(wctx, 2*time.Second, true, func(context.Context) (bool, error) {
		_, statErr := os.Stat(pem)
		return statErr == nil, nil
	})
	if err != nil {
		logf("kubelet rotation store not ready yet; kubelet.conf finalize deferred to next boot")
		return nil
	}
	changed, err := kubeadm.FinalizeKubeletKubeconfig(l)
	if err != nil {
		return fmt.Errorf("finalize kubelet.conf: %w", err)
	}
	if !changed {
		return nil
	}
	logf("kubelet.conf repointed at kubelet-client-current.pem; restarting kubelet")
	cmd := exec.CommandContext(ctx, "systemctl", "restart", "kubelet.service")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart kubelet: %v: %s", err, out)
	}
	return nil
}
