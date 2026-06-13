package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/MatchaScript/nanokube/internal/bootstraptoken"
	"github.com/MatchaScript/nanokube/internal/kubeclient"
)

func newTokenCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage bootstrap tokens for joining nodes",
	}
	var ttl time.Duration
	create := &cobra.Command{
		Use:   "create",
		Short: "Mint a bootstrap token and print the add-node credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := kubeclient.LoadAdmin(g.layout.AdminKubeconfig)
			if err != nil {
				return err
			}
			res, err := bootstraptoken.Create(client, ttl, filepath.Join(g.layout.PKIDir, "ca.crt"))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "token: %s\n", res.Token)
			fmt.Fprintf(out, "ca-cert-hash: %s\n", res.CACertHash)
			fmt.Fprintf(out, "on the joining node:\n  nanokube add-node --server https://<this-node-or-endpoint>:6443 --token %s --ca-cert-hash %s\n", res.Token, res.CACertHash)
			return nil
		},
	}
	create.Flags().DurationVar(&ttl, "ttl", 24*time.Hour, "token lifetime; expired tokens are garbage-collected by the cluster")
	cmd.AddCommand(create)
	return cmd
}
