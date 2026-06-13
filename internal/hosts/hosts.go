// Package hosts manages the single nanokube-managed /etc/hosts entry
// that backs the cluster's stable controlPlaneEndpoint name on nodes
// without real DNS for it (Tier 1: failover is by rewrite only).
package hosts

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

const marker = "# nanokube-managed"

const path = "/etc/hosts"

// EnsureEntry makes endpoint's host part resolve on this node by
// pinning it to ip in /etc/hosts. No-op when the endpoint is empty or
// an IP. A pre-existing nanokube-managed entry is refreshed when ip
// changed — the resolve check must not run first, or it would see our
// own stale pin and bail. An unmanaged name that already resolves is
// left alone. endpoint may carry a :port suffix.
func EnsureEntry(endpoint, ip string, logf func(string, ...any)) error {
	if endpoint == "" {
		return nil
	}
	host := endpoint
	if h, _, err := net.SplitHostPort(endpoint); err == nil {
		host = h
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current := string(b)
	if !hasManagedEntry(current, host) {
		if _, err := net.LookupHost(host); err == nil {
			return nil
		}
	}
	if net.ParseIP(ip) == nil {
		logf("warning: %s does not resolve and %q is not an IP; fix DNS or /etc/hosts manually", host, ip)
		return nil
	}
	updated := upsertEntry(current, host, ip)
	if updated == current {
		return nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	logf("pinned %s -> %s in %s", host, ip, path)
	return nil
}

// hasManagedEntry reports whether hosts content already carries a
// nanokube-managed line for name.
func hasManagedEntry(hosts, name string) bool {
	for _, line := range strings.Split(hosts, "\n") {
		if strings.HasSuffix(line, marker) {
			if f := strings.Fields(line); len(f) >= 2 && f[1] == name {
				return true
			}
		}
	}
	return false
}

// upsertEntry returns hosts content with exactly one nanokube-managed
// line mapping name -> ip; everything else is preserved verbatim.
func upsertEntry(hosts, name, ip string) string {
	entry := fmt.Sprintf("%s %s %s", ip, name, marker)
	lines := strings.Split(strings.TrimRight(hosts, "\n"), "\n")
	out := make([]string, 0, len(lines)+1)
	replaced := false
	for _, line := range lines {
		if strings.HasSuffix(line, marker) && len(strings.Fields(line)) >= 2 && strings.Fields(line)[1] == name {
			if !replaced {
				out = append(out, entry)
				replaced = true
			}
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		out = append(out, entry)
	}
	return strings.Join(out, "\n") + "\n"
}
