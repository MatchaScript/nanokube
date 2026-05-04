package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/MatchaScript/nanokube/internal/teardown"
)

func newResetCmd(_ *globalOpts) *cobra.Command {
	var confirm bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Tear down all nanokube-managed state (matches `kubeadm reset`)",
		Long: "Stops kubelet, removes CRI containers and pod sandboxes, wipes " +
			"/etc/kubernetes, /var/lib/etcd, /var/lib/kubelet, /var/lib/nanokube, " +
			"deletes CNI network interfaces, and flushes iptables and ipvs rules. " +
			"Intended for test beds or when re-initialising from scratch.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !confirm {
				return errors.New("refusing to proceed without --yes (this is destructive)")
			}
			return teardown.Run(cmd.Context(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&confirm, "yes", false, "confirm the destructive operation")
	return cmd
}
