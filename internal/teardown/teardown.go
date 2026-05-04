// Package teardown implements `nanokube reset`: tearing the node back
// down to the state a fresh `nanokube init` would expect. The flow
// mirrors `kubeadm reset --force`:
//
//  1. Stop kubelet (so static pods are not restarted mid-cleanup).
//  2. Remove every CRI container, then every pod sandbox, via crictl.
//     Sandboxes are removed last so the pause containers release their
//     netns / mount refs cleanly.
//  3. Lazy-unmount everything kubelet bind/tmpfs-mounted under
//     /var/lib/kubelet (projected service-account tokens, csi mounts,
//     …). Without this step os.RemoveAll trips EBUSY on the projected
//     volumes.
//  4. Remove the managed filesystem paths (/etc/kubernetes,
//     /var/lib/etcd, /var/lib/kubelet, /var/lib/nanokube).
//  5. Delete CNI virtual interfaces (cni0, flannel.1, kube-ipvs0, …) so
//     the next cluster's CNI starts from a clean slate.
//  6. Flush iptables (filter / nat / mangle chains + user-defined
//     chains) and ipvs rules. nftables rules are NOT auto-flushed
//     (matches kubeadm reset); a warning is emitted if `nft` is present
//     so operators know to run `nft flush ruleset` themselves.
package teardown

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/MatchaScript/nanokube/internal/paths"
)

// Logger is a tiny printf-shaped sink. nil is treated as a no-op so
// callers that do not care about progress output can pass nil.
type Logger func(format string, a ...any)

// Run executes the full teardown. RemoveAll on the managed paths is the
// only fatal step; every other phase is best-effort and continues on
// error so a partially broken host can still be cleaned up.
func Run(ctx context.Context, out io.Writer) error {
	logf := func(format string, a ...any) {
		if out == nil {
			return
		}
		fmt.Fprintf(out, "[reset] "+format+"\n", a...)
	}

	stopKubelet(ctx, logf)
	cleanupCRIContainers(ctx, logf)
	cleanupCRIPodSandboxes(ctx, logf)
	unmountKubeletMounts(logf)

	for _, t := range []string{
		paths.KubernetesDir,
		paths.EtcdDataDir,
		paths.KubeletDir,
		paths.NanoKubeVarDir,
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.RemoveAll(t); err != nil {
			return fmt.Errorf("remove %s: %w", t, err)
		}
		logf("removed %s", t)
	}

	deleteCNIInterfaces(ctx, logf)
	flushIptables(ctx, logf)
	flushIPVS(ctx, logf)
	warnNftablesRules(logf)

	return nil
}

// stopKubelet stops kubelet.service so static pods are not brought back
// up mid-cleanup. Non-fatal: on a fresh node kubelet may not be
// installed or enabled.
func stopKubelet(ctx context.Context, logf Logger) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "systemctl", "stop", "kubelet.service")
	if out, err := cmd.CombinedOutput(); err != nil {
		logf("systemctl stop kubelet.service (continuing): %v: %s", err, strings.TrimSpace(string(out)))
		return
	}
	logf("stopped kubelet.service")
}

// cleanupCRIContainers asks crictl (wired to the CRI-O socket) to stop
// and remove every container on the node. kubelet owns pod lifecycle in
// normal operation, but after `systemctl stop kubelet` the containers
// linger and hold open files inside /var/lib/kubelet until crictl rm.
func cleanupCRIContainers(ctx context.Context, logf Logger) {
	if _, err := exec.LookPath("crictl"); err != nil {
		logf("crictl not found, skipping container cleanup")
		return
	}
	ids, err := crictlListIDs(ctx, "ps", "--all", "--quiet")
	if err != nil {
		logf("list CRI containers (continuing): %v", err)
		return
	}
	if len(ids) == 0 {
		logf("no CRI containers to remove")
		return
	}
	if err := crictlRun(ctx, append([]string{"stop", "--timeout", "5"}, ids...)...); err != nil {
		logf("crictl stop (continuing): %v", err)
	}
	if err := crictlRun(ctx, append([]string{"rm", "--force"}, ids...)...); err != nil {
		logf("crictl rm (continuing): %v", err)
	}
	logf("removed %d CRI containers", len(ids))
}

// cleanupCRIPodSandboxes removes pod sandboxes after their containers
// have been removed. The pause container in each sandbox holds the
// netns and any volume mounts; without `crictl rmp` they linger and can
// keep /var/lib/kubelet entries pinned.
func cleanupCRIPodSandboxes(ctx context.Context, logf Logger) {
	if _, err := exec.LookPath("crictl"); err != nil {
		return
	}
	ids, err := crictlListIDs(ctx, "pods", "--quiet")
	if err != nil {
		logf("list CRI pod sandboxes (continuing): %v", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	if err := crictlRun(ctx, append([]string{"rmp", "--force"}, ids...)...); err != nil {
		logf("crictl rmp (continuing): %v", err)
		return
	}
	logf("removed %d CRI pod sandboxes", len(ids))
}

func crictlListIDs(ctx context.Context, args ...string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "crictl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("crictl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	var ids []string
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		id := strings.TrimSpace(scanner.Text())
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, scanner.Err()
}

func crictlRun(ctx context.Context, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "crictl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("crictl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// unmountKubeletMounts lazy-detaches every mountpoint kubelet placed
// under /var/lib/kubelet — projected service-account tokens are tmpfs
// bind-mounts that survive `systemctl stop kubelet`, so a plain
// os.RemoveAll trips EBUSY on them. Mirrors kubeadm's
// cmd/kubeadm/app/cmd/phases/reset/unmount_linux.go.
//
// Children are unmounted before parents (reverse-sorted by path) so
// nested binds can detach cleanly. Failures are logged but never fatal:
// MNT_DETACH already releases the namespace ref so the subsequent
// RemoveAll succeeds even when the kernel keeps the mount alive briefly.
func unmountKubeletMounts(logf Logger) {
	raw, err := os.ReadFile("/proc/mounts")
	if err != nil {
		logf("read /proc/mounts (continuing): %v", err)
		return
	}
	prefix := paths.KubeletDir
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var targets []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, " ")
		if len(fields) < 2 || !strings.HasPrefix(fields[1], prefix) {
			continue
		}
		targets = append(targets, fields[1])
	}
	sort.Sort(sort.Reverse(sort.StringSlice(targets)))
	unmounted := 0
	for _, t := range targets {
		if err := syscall.Unmount(t, syscall.MNT_DETACH); err != nil {
			logf("unmount %s (continuing): %v", t, err)
			continue
		}
		unmounted++
	}
	if unmounted > 0 {
		logf("unmounted %d kubelet mounts under %s", unmounted, paths.KubeletDir)
	}
}

// cniInterfaces enumerates the virtual network interfaces that CNI
// plugins typically create. kubeadm reset hard-codes a similar list.
var cniInterfaces = []string{
	"cni0",
	"flannel.1",
	"cilium_host",
	"cilium_net",
	"cilium_vxlan",
	"kube-ipvs0",
	"dummy0",
	"weave",
	"vxlan.calico",
}

func deleteCNIInterfaces(ctx context.Context, logf Logger) {
	if _, err := exec.LookPath("ip"); err != nil {
		logf("iproute2 `ip` not found, skipping CNI interface cleanup")
		return
	}
	for _, name := range cniInterfaces {
		if !interfaceExists(ctx, name) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cmd := exec.CommandContext(cctx, "ip", "link", "delete", name)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			logf("ip link delete %s (continuing): %v: %s", name, err, strings.TrimSpace(string(out)))
			continue
		}
		logf("deleted interface %s", name)
	}
}

func interfaceExists(ctx context.Context, name string) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ip", "link", "show", name)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// flushIptables runs iptables -F / -X on the three tables kubeadm/CNI
// plugins touch. Missing iptables binary is not fatal (hosts using pure
// nftables may omit it).
func flushIptables(ctx context.Context, logf Logger) {
	if _, err := exec.LookPath("iptables"); err != nil {
		logf("iptables not found, skipping iptables flush")
		return
	}
	tables := []string{"filter", "nat", "mangle"}
	for _, t := range tables {
		for _, op := range [][]string{{"-F"}, {"-X"}} {
			args := append([]string{"-t", t}, op...)
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			cmd := exec.CommandContext(cctx, "iptables", args...)
			out, err := cmd.CombinedOutput()
			cancel()
			if err != nil {
				logf("iptables %s (continuing): %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
		}
	}
	logf("flushed iptables (filter/nat/mangle)")
}

// warnNftablesRules emits a hint when `nft` is installed: kube-proxy
// nft mode and modern CNIs (Cilium, recent Calico) keep their state in
// nftables, but kubeadm reset deliberately does not auto-flush nft —
// blowing away the user's entire ruleset is too aggressive for a
// cluster-cleanup tool. We mirror that posture and just warn so
// operators know to run `nft flush ruleset` themselves if needed.
func warnNftablesRules(logf Logger) {
	if _, err := exec.LookPath("nft"); err != nil {
		return
	}
	logf("nft present: nftables rules left untouched. " +
		"If kube-proxy or your CNI uses nftables, run `nft flush ruleset` manually.")
}

// flushIPVS clears the IPVS table kube-proxy uses in IPVS mode. ipvsadm
// may not be installed on iptables-only clusters; treat as optional.
func flushIPVS(ctx context.Context, logf Logger) {
	if _, err := exec.LookPath("ipvsadm"); err != nil {
		logf("ipvsadm not found, skipping IPVS flush")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ipvsadm", "-C")
	if out, err := cmd.CombinedOutput(); err != nil {
		logf("ipvsadm -C (continuing): %v: %s", err, strings.TrimSpace(string(out)))
		return
	}
	logf("flushed IPVS rules")
}
