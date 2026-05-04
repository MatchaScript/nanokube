package healthcheck

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
)

// WaitForAPIServer polls the apiserver's /readyz endpoint until it
// succeeds or ctx is cancelled. Tolerates the apiserver static pod
// not yet being up. The caller controls the deadline via ctx.
func WaitForAPIServer(ctx context.Context, client kubernetes.Interface) error {
	var lastErr error
	for {
		_, err := client.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("apiserver not ready: %w (last probe: %v)", ctx.Err(), lastErr)
			}
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
