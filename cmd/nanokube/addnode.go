package main

import (
	"github.com/spf13/cobra"

	"github.com/MatchaScript/nanokube/internal/addnode"
	"github.com/MatchaScript/nanokube/internal/version"
)

// newAddNodeCmd is the worker-join verb: discover the cluster with the
// bootstrap token + CA pin from `nanokube token create`, cache the
// cluster config locally, then hand off to `nanokube boot` via a blocking
// service restart. A successful restart is join confirmation.
func newAddNodeCmd(g *globalOpts) *cobra.Command {
	var opts addnode.Options
	cmd := &cobra.Command{
		Use:   "add-node",
		Short: "Join this node to an existing cluster as a worker",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return addnode.Run(cmd.Context(), opts, g.layout, version.KubernetesVersion, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.Server, "server", "", "reachable control-plane apiserver (https://host:port)")
	cmd.Flags().StringVar(&opts.Token, "token", "", "bootstrap token from `nanokube token create`")
	cmd.Flags().StringSliceVar(&opts.CACertHashes, "ca-cert-hash", nil, "CA public key pin (sha256:...) from `nanokube token create`")
	_ = cmd.MarkFlagRequired("server")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("ca-cert-hash")
	return cmd
}
