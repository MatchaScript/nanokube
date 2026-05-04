package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MatchaScript/nanokube/internal/paths"
	"github.com/MatchaScript/nanokube/internal/version"
)

type globalOpts struct {
	configPath string
}

func newRootCmd() *cobra.Command {
	opts := &globalOpts{}

	cmd := &cobra.Command{
		Use:           "nanokube",
		Short:         "Minimal single-node Kubernetes for bootc-style edge deployments",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().StringVar(&opts.configPath, "config", paths.ConfigFile, "path to NanoKubeConfig YAML")

	cmd.AddCommand(
		newInitCmd(opts),
		newResetCmd(opts),
		newBootCmd(opts), // hidden, invoked by nanokube.service
		newHealthcheckCmd(opts),
		newConfigCmd(opts),
		newKubeconfigCmd(opts),
		newVersionCmd(),
	)
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build and target versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "nanokube   kubernetes=%s commit=%s built=%s\n",
				version.KubernetesVersion, version.GitCommit, version.BuildDate)
			return nil
		},
	}
}
