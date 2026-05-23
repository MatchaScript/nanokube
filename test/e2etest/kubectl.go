package e2etest

import (
	"bytes"
	"fmt"
	"os/exec"
	"time"
)

// Kubectl runs kubectl with KUBECONFIG set, fails the test on non-zero
// exit, returns stdout.
func (h *Helpers) Kubectl(args ...string) string {
	h.t.Helper()
	out, err := h.KubectlRaw(args...)
	if err != nil {
		h.t.Fatalf("kubectl %v: %v\noutput: %s", args, err, out)
	}
	return out
}

// KubectlRaw runs kubectl and returns combined output + err.
func (h *Helpers) KubectlRaw(args ...string) (string, error) {
	var so, se bytes.Buffer
	cmd := exec.Command("kubectl", args...)
	cmd.Env = append(cmd.Environ(), "KUBECONFIG="+h.kubeconfig)
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		return so.String() + se.String(), fmt.Errorf("%w: %s", err, se.String())
	}
	return so.String(), nil
}

// WaitForNodeReady blocks until every node has condition=Ready=True or
// the timeout fires.
func (h *Helpers) WaitForNodeReady(timeout time.Duration) {
	h.t.Helper()
	h.Kubectl("wait", "--for=condition=Ready", "node", "--all", "--timeout="+timeout.String())
}

// WaitForPodsReady blocks until every pod in the namespace is Ready.
// Retries up to 3 times with a 5s delay: on a freshly-booted cluster
// the first wait can race against pod creation that has not yet hit
// the apiserver.
func (h *Helpers) WaitForPodsReady(namespace string, timeout time.Duration) {
	h.t.Helper()
	err := Retry(3, 5*time.Second, func() error {
		_, e := h.KubectlRaw("wait", "--for=condition=Ready", "pods", "--all",
			"-n", namespace, "--timeout="+timeout.String())
		return e
	})
	if err != nil {
		h.t.Fatalf("WaitForPodsReady %s: %v", namespace, err)
	}
}

// WaitForStaticPodReady blocks until a single named pod is Ready.
func (h *Helpers) WaitForStaticPodReady(podName, ns string, timeout time.Duration) {
	h.t.Helper()
	h.Kubectl("wait", "--for=condition=Ready", "pod/"+podName,
		"-n", ns, "--timeout="+timeout.String())
}

// FlannelURL returns the configured flannel manifest URL.
func (h *Helpers) FlannelURL() string { return h.flannelURL }
