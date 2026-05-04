package main

import (
	"fmt"

	"github.com/spf13/cobra"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/config"
	"github.com/MatchaScript/nanokube/internal/version"
)

func newConfigCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate NanoKubeConfig",
	}
	cmd.AddCommand(newConfigPrintDefaultsCmd(), newConfigValidateCmd(g))
	return cmd
}

func newConfigPrintDefaultsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "print-defaults",
		Short: "Print a NanoKubeConfig with all defaults applied",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := config.Marshal(v1alpha1.NewDefault())
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
}

func newConfigValidateCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Load the config file, apply defaults, and validate it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(g.configPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "config %s is valid (kubernetesVersion=%s, advertiseAddress=%s)\n",
				g.configPath, version.KubernetesVersion, cfg.Spec.ControlPlane.AdvertiseAddress)
			return nil
		},
	}
}
