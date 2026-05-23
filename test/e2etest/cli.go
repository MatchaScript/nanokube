package e2etest

import (
	"bytes"
	"os/exec"
)

// Nanokube runs the nanokube binary with args. Fails the test on
// non-zero exit. Returns captured stdout and stderr.
func (h *Helpers) Nanokube(args ...string) (stdout, stderr string) {
	h.t.Helper()
	stdout, stderr, err := h.NanokubeRaw(args...)
	if err != nil {
		h.t.Fatalf("nanokube %v: %v\nstdout: %s\nstderr: %s", args, err, stdout, stderr)
	}
	return stdout, stderr
}

// NanokubeExpectFail runs the binary and fails the test if it exits 0.
func (h *Helpers) NanokubeExpectFail(args ...string) (stdout, stderr string) {
	h.t.Helper()
	stdout, stderr, err := h.NanokubeRaw(args...)
	if err == nil {
		h.t.Fatalf("nanokube %v: expected failure but exited 0\nstdout: %s\nstderr: %s",
			args, stdout, stderr)
	}
	return stdout, stderr
}

// NanokubeRaw runs the binary and returns stdout, stderr, and the
// process error (nil on exit 0).
func (h *Helpers) NanokubeRaw(args ...string) (stdout, stderr string, err error) {
	var so, se bytes.Buffer
	cmd := exec.Command(h.bin, args...)
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}
