// Package lifecycle implements `nanokube boot`: the pure-reconcile flow
// nanokube.service invokes on every reboot of an already-initialised
// node. One-time initialisation (PKI seeding, cluster-admins CRB,
// super-admin.conf lifecycle) lives in package initialize and is run
// by `nanokube init`; Boot deliberately knows nothing about it and
// uses admin.conf alone — never falling back to super-admin.conf, which
// has been deleted by the time Boot ever runs. Aligned with microshift's
// prerun flow (reference/microshift/pkg/admin/prerun/prerun.go):
//
//  1. If the greenboot red.d hook left a restore marker, pick the
//     newest backup for the currently-booted deployment and restore
//     its data trees (etcd, /etc/kubernetes, selected kubelet files)
//     before running any reconcile logic.
//  2. Take a snapshot of the data on disk NOW (which is whatever the
//     last successful boot wrote) so that a future rollback into that
//     deployment has a backup to pick up. The backup is named after the
//     previous boot's deployment+boot ids recorded in last-boot.json.
//  3. Reconcile via kubeadm phases (Ensure), start kubelet, poll
//     /readyz, wait for node + control-plane static pods Ready, mark
//     the control-plane node (idempotent: picks up taint changes in
//     NanoKubeConfig), reconcile addons (best effort), then notify
//     systemd READY=1 so a blocking `systemctl start` only returns
//     once the cluster is actually usable.
//  4. Prune backups belonging to deployments that bootc has GCed.
//  5. Update last-boot.json and last-event. Caller idles until SIGTERM.
//
// nanokube.service is Type=notify and stays Active(running) once Boot
// returns nil — the binary blocks in the caller after a healthy boot
// rather than exiting. The unit deliberately does NOT declare
// Before=kubelet.service: while we're still 'activating' systemd would
// queue our own inline `systemctl start kubelet.service` job behind
// that activation and deadlock. Instead the kubelet unit we ship
// carries no [Install] section, so multi-user.target cannot pull it
// in ahead of nanokube — kubelet only ever runs because nanokube asked.
//
// Greenboot's required.d/ judges boot success against the live
// control plane via `nanokube healthcheck`, not against this service's
// exit code. That decoupling lets Boot treat tail bookkeeping
// (last-boot.json, last-event) as best-effort: a transient write
// failure after the cluster is verified healthy logs a warning but
// does not flip a working cluster into rollback. Failures earlier in
// the pipeline (Ensure / kubelet / readyz / control-plane wait) still
// propagate as non-zero exit because the service is then genuinely
// broken — `nanokube healthcheck` would also fail, and the systemctl
// is-active gate in required.d catches the service itself being dead.
// The rollback intent is conveyed by greenboot red.d touching the
// restore marker just before bootc rolls back — there is no self-set
// "rollback-needed" flag.
//
// Non-ostree systems (no /run/ostree-booted) still run Ensure + kubelet
// but skip backup/restore entirely; atomic rollback is physically
// unavailable without an ostree/bootc deployment model.
package lifecycle

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"k8s.io/client-go/kubernetes"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/markcontrolplane"

	"github.com/MatchaScript/nanokube/internal/backup"
	"github.com/MatchaScript/nanokube/internal/certs"
	"github.com/MatchaScript/nanokube/internal/healthcheck"
	"github.com/MatchaScript/nanokube/internal/kubeadm"
	"github.com/MatchaScript/nanokube/internal/kubeclient"
	"github.com/MatchaScript/nanokube/internal/ostree"
	"github.com/MatchaScript/nanokube/internal/paths"
	"github.com/MatchaScript/nanokube/internal/preflight"
	"github.com/MatchaScript/nanokube/internal/state"
)

// Boot runs the per-reboot reconcile flow. out receives human-readable
// progress logs (journald when invoked from systemd). Returns nil on a
// healthy boot; any non-nil error means nanokube.service will exit
// non-zero, which greenboot's required.d/ script turns into a boot
// failure. Boot assumes the node has already been initialised — first
// initialisation lives in package initialize. Boot uses admin.conf
// only; super-admin.conf has been deleted by then and any operator who
// regenerated it via `nanokube kubeconfig super-admin` is responsible
// for cleaning it up themselves.
func Boot(ctx context.Context, cfg *kubeadmapi.InitConfiguration, selfVersion string, out io.Writer) error {
	logf := func(format string, a ...any) { fmt.Fprintf(out, "[nanokube] "+format+"\n", a...) }
	nodeName := cfg.NodeRegistration.Name

	// Preflight gates writability + free-space AND stages a clean
	// workspace BEFORE Ensure / kubelet start. The defer cleanup() is
	// the single point of truth for wiping any partial backup staging
	// — backup.Create itself just returns errors and stops, never
	// rolls back its own scratch. The cleanup is a no-op once a
	// successful Create has renamed the staging dir to its final name.
	workspace, cleanup, err := preflight.AllocateWorkspace()
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	defer cleanup()
	useBackups := workspace.UseBackups

	currentDeployment := ""
	if useBackups {
		currentDeployment, err = ostree.BootedDeploymentID()
		if err != nil {
			return fmt.Errorf("booted deployment id: %w", err)
		}
	} else {
		logf("non-ostree system: backup/restore disabled")
	}

	currentBoot, err := backup.BootID()
	if err != nil {
		return err
	}

	if useBackups {
		if err := maybeRestore(currentDeployment, logf); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
	}

	prev, hadPrev, err := state.ReadLastBoot()
	if err != nil {
		return err
	}

	// Snapshot the data the previous successful boot left on disk, named
	// after that boot's (deployment, boot) ids. On the first ever boot
	// there is nothing to snapshot. On a rollback boot we may have just
	// restored; in that case the backup by this name already exists and
	// Create skips.
	if useBackups && hadPrev && prev.DeploymentID != "" && prev.BootID != "" && prev.BootID != currentBoot {
		finalDir := filepath.Join(paths.BackupsDir, backup.Name(prev))
		if err := backup.Create(workspace.BackupTmp, finalDir, prev); err != nil {
			return fmt.Errorf("create backup: %w", err)
		}
		logf("snapshot of previous boot saved as %s", shortPair(prev.DeploymentID, prev.BootID))
	}

	upgrading := hadPrev && prev.Version != selfVersion
	switch {
	case !hadPrev:
		logf("first healthy boot pending (version=%s)", selfVersion)
	case upgrading:
		logf("upgrade path: %s -> %s", prev.Version, selfVersion)
		_ = state.WriteLastEvent(fmt.Sprintf("upgrading %s -> %s", prev.Version, selfVersion))
	default:
		logf("reconcile path (version=%s)", selfVersion)
	}

	if err := kubeadm.Ensure(cfg, kubeadm.DefaultLayout()); err != nil {
		return bootFailed(upgrading, prev.Version, selfVersion, fmt.Errorf("ensure: %w", err))
	}

	if err := rotateCertsIfStale(cfg, certsLayout(kubeadm.DefaultLayout()), out); err != nil {
		return bootFailed(upgrading, prev.Version, selfVersion, err)
	}

	if err := startKubelet(ctx, logf); err != nil {
		return bootFailed(upgrading, prev.Version, selfVersion, err)
	}

	if err := waitReadyz(ctx, logf); err != nil {
		return bootFailed(upgrading, prev.Version, selfVersion, err)
	}

	// admin.conf only: the cluster-admins CRB was seeded once during
	// `nanokube init` (package initialize) and super-admin.conf was
	// removed at that time. If the CRB has since been deleted by hand,
	// admin.conf cannot reach the apiserver and waitControlPlane will
	// surface the failure — recovery is `nanokube reset` + `init`, not
	// a silent fallback.
	client, err := kubeclient.LoadAdmin(paths.AdminKubeconfig)
	if err != nil {
		return bootFailed(upgrading, prev.Version, selfVersion, err)
	}

	// Beyond /readyz on the apiserver, confirm the node object reports
	// Ready=True and each of the three control-plane static pods is
	// Ready. Matches kinder's waitNewControlPlaneNodeReady and catches
	// CM/scheduler crash-loops that /readyz alone would miss.
	if err := waitControlPlane(ctx, client, nodeName, logf); err != nil {
		return bootFailed(upgrading, prev.Version, selfVersion, err)
	}

	if err := markcontrolplane.MarkControlPlane(client, nodeName, cfg.NodeRegistration.Taints); err != nil {
		return bootFailed(upgrading, prev.Version, selfVersion, fmt.Errorf("mark control-plane: %w", err))
	}

	if err := kubeadm.EnsureAddons(cfg, client, out); err != nil {
		logf("addon reconcile failed (continuing): %v", err)
	}

	if useBackups {
		if deployments, err := ostree.AllDeploymentIDs(); err != nil {
			logf("list deployments failed (skipping prune): %v", err)
		} else if err := backup.Prune(deployments); err != nil {
			logf("prune failed (continuing): %v", err)
		}
	}

	// Cluster is verified healthy by this point. Treat last-boot.json
	// as bookkeeping: a transient write failure must NOT propagate to a
	// non-zero exit, because greenboot's required.d now judges boot
	// health via `nanokube healthcheck` against the live apiserver, not
	// via this service's exit code. The cost of a missed write is one
	// boot of stale `prev` next time around (snapshot may use an older
	// name; backup.Create is idempotent on duplicates) — strictly less
	// damaging than rolling back a healthy cluster.
	if err := state.WriteLastBoot(state.LastBoot{
		Version:      selfVersion,
		DeploymentID: currentDeployment,
		BootID:       currentBoot,
	}); err != nil {
		logf("write last-boot failed (continuing): %v", err)
	}
	switch {
	case upgrading:
		_ = state.WriteLastEvent(fmt.Sprintf("upgraded %s -> %s", prev.Version, selfVersion))
	case !hadPrev:
		_ = state.WriteLastEvent(fmt.Sprintf("initialised at %s", selfVersion))
	default:
		_ = state.WriteLastEvent(fmt.Sprintf("healthy at %s", selfVersion))
	}
	// Cluster is verified healthy. Notify systemd READY=1 so a blocking
	// `systemctl start nanokube.service` returns only once the system is
	// actually usable. The unit deliberately does NOT carry
	// Before=kubelet.service: that would make systemd queue the kubelet
	// start job we issue from inside startKubelet behind our own
	// activation, deadlocking the readyz wait. Instead we keep kubelet
	// from racing ahead by ensuring kubelet.service ships without an
	// [Install] section, so multi-user.target cannot pull it in
	// independently of nanokube.
	notifyReady(logf)
	logf("boot complete")
	return nil
}

// maybeRestore consumes the greenboot-placed restore marker. If a
// backup matching the currently-booted deployment exists it is restored
// and its meta.json is written to last-boot.json so the rest of this
// boot sees the restored state as the "previous boot". The marker is
// always cleared so a stray marker cannot cause repeated restores.
func maybeRestore(currentDeployment string, logf func(string, ...any)) error {
	requested, err := backup.RestoreRequested()
	if err != nil {
		return err
	}
	if !requested {
		return nil
	}
	defer func() {
		if err := backup.ClearRestoreMarker(); err != nil {
			logf("clear restore marker failed: %v", err)
		}
	}()

	if currentDeployment == "" {
		logf("restore marker present but no booted deployment id; ignoring")
		_ = state.WriteLastEvent("restore requested but no deployment id")
		return nil
	}
	name, err := backup.LatestForDeployment(currentDeployment)
	if err != nil {
		return err
	}
	if name == "" {
		logf("restore marker present but no backup for deployment %s", shortID(currentDeployment))
		_ = state.WriteLastEvent("restore requested but no backup for current deployment")
		return nil
	}

	logf("restoring backup %s", name)
	if err := backup.Restore(name); err != nil {
		return err
	}
	meta, err := backup.ReadMeta(name)
	if err != nil {
		return err
	}
	if err := state.WriteLastBoot(meta); err != nil {
		return err
	}
	_ = state.WriteLastEvent(fmt.Sprintf("restored backup %s", name))
	return nil
}

func bootFailed(upgrading bool, prev, self string, cause error) error {
	reason := cause.Error()
	switch {
	case upgrading:
		_ = state.WriteLastEvent(fmt.Sprintf("boot failed upgrading %s -> %s: %s", prev, self, reason))
	default:
		_ = state.WriteLastEvent(fmt.Sprintf("boot failed at %s: %s", self, reason))
	}
	return cause
}

// notifyReady sends sd_notify READY=1 if running under a systemd unit
// with Type=notify. Outside systemd (e.g. unit tests, manual `nanokube
// boot` invocation) it is a no-op. We pass unsetEnvironment=true so
// that the systemctl/kubeadm processes we exec afterwards do not
// inherit NOTIFY_SOCKET and accidentally re-send readiness on our
// behalf.
func notifyReady(logf func(string, ...any)) {
	sent, err := daemon.SdNotify(true, daemon.SdNotifyReady)
	switch {
	case err != nil:
		logf("sd_notify READY=1 failed (continuing): %v", err)
	case sent:
		logf("sd_notify READY=1 sent")
	}
}

// startKubelet asks systemd to start kubelet.service without blocking
// on its readiness. Readiness is verified separately via /readyz.
func startKubelet(ctx context.Context, logf func(string, ...any)) error {
	cmd := exec.CommandContext(ctx, "systemctl", "start", "--no-block", "kubelet.service")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl start kubelet: %v: %s", err, out)
	}
	logf("kubelet.service queued for start")
	return nil
}

// readyzTimeout bounds how long we wait for apiserver /readyz after
// asking systemd to start kubelet. A stall here triggers the
// boot-failed path.
const readyzTimeout = 3 * time.Minute

func waitReadyz(ctx context.Context, logf func(string, ...any)) error {
	logf("waiting for apiserver /readyz (timeout=%s)", readyzTimeout)
	client, err := kubeclient.LoadAdmin(paths.AdminKubeconfig)
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

// controlPlaneTimeout bounds how long we wait for node Ready + the three
// control-plane static pods Ready once the apiserver itself responded to
// /readyz. Generous because CM/scheduler may take a few iterations after
// leader election + ServiceAccount token availability.
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

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func shortPair(deploy, boot string) string {
	return shortID(deploy) + "_" + shortID(boot)
}

// rotateCertsIfStale is the per-boot rotation hook called between
// kubeadm.Ensure (which depends on a complete PKI) and startKubelet
// (which reads the rotated cert files on first start).
//
// Two passes:
//  1. Check every CA. Any CA whose remaining lifetime trips
//     certs.NeedsRotation is regenerated together with every leaf and
//     kubeconfig that chains back to it (cascade).
//  2. Check every leaf. Anything still stale (CA was healthy, leaf was
//     not) is renewed in place via the kubeadm renewal manager.
//
// Step 2 is naturally idempotent against step 1: leaves regenerated by
// the CA cascade have a fresh NotAfter and will not trip NeedsRotation
// on the re-read in step 2.
func rotateCertsIfStale(cfg *kubeadmapi.InitConfiguration, layout certs.Layout, out io.Writer) error {
	logf := func(format string, a ...any) { fmt.Fprintf(out, "[nanokube] "+format+"\n", a...) }

	caReport, err := certs.CheckCAs(layout)
	if err != nil {
		return fmt.Errorf("check CAs: %w", err)
	}
	signer := certs.NewSigner(cfg, layout)
	for _, ca := range certs.AllCAs() {
		exp, ok := caReport[ca]
		if !ok || exp.NotFound {
			return fmt.Errorf("CA %s missing at boot: %s", ca, exp.Path)
		}
		if !certs.NeedsRotation(exp.Cert) {
			continue
		}
		logf("rotating CA %s (remaining=%s)", ca, exp.Remaining)
		if err := signer.RegenerateCA(ca); err != nil {
			return fmt.Errorf("regenerate CA %s: %w", ca, err)
		}
	}

	leafReport, err := certs.CheckLeaves(cfg, layout)
	if err != nil {
		return fmt.Errorf("check leaves: %w", err)
	}
	var stale []certs.LeafKind
	for _, leaf := range certs.AllLeaves() {
		exp, ok := leafReport[leaf]
		if !ok || exp.NotFound {
			continue
		}
		if certs.NeedsRotation(exp.Cert) {
			stale = append(stale, leaf)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	logf("renewing %d expiring leaves", len(stale))
	if err := signer.RenewLeaves(stale); err != nil {
		return fmt.Errorf("renew leaves: %w", err)
	}
	return nil
}

// certsLayout maps lifecycle's kubeadm.Layout into the certs package
// layout.
func certsLayout(l kubeadm.Layout) certs.Layout {
	return certs.Layout{
		PKIDir:        l.PKIDir,
		KubeconfigDir: l.KubeconfigDir,
	}
}
