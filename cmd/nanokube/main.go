package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// TODO: switch to k8s.io/component-base/cli.Run + genericclioptions.IOStreams
// once we embed client-go / kubeadm libraries. See
// reference/microshift/cmd/microshift/main.go for the canonical shape:
// cli.Run handles signal forwarding, klog flushing, panic recovery and
// cobra-error → exit-code mapping in one call, and IOStreams lets every
// subcommand take In/Out/ErrOut by injection rather than reaching into
// cmd.OutOrStdout(). Doing this before klog ships into our binary avoids
// retrofitting log-flush handling later.
func main() {
	// SIGTERM handler is required for `nanokube boot`, which parks on
	// ctx.Done() after a healthy boot to keep nanokube.service active.
	// Other subcommands ignore the cancellation but inherit it for free.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
