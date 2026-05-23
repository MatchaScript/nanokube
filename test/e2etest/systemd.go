package e2etest

import (
	"bytes"
	"os/exec"
	"strings"
)

// SystemctlStart runs `systemctl start <unit>`; fatal on error.
func (h *Helpers) SystemctlStart(unit string) {
	h.t.Helper()
	h.systemctl("start", unit)
}

// SystemctlRestart runs `systemctl restart <unit>`; fatal on error.
func (h *Helpers) SystemctlRestart(unit string) {
	h.t.Helper()
	h.systemctl("restart", unit)
}

// SystemctlIsActive returns true if `systemctl is-active --quiet` succeeds.
func (h *Helpers) SystemctlIsActive(unit string) bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", unit)
	return cmd.Run() == nil
}

func (h *Helpers) systemctl(args ...string) {
	h.t.Helper()
	var so, se bytes.Buffer
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		h.t.Fatalf("systemctl %s: %v\nstderr: %s",
			strings.Join(args, " "), err, se.String())
	}
}
