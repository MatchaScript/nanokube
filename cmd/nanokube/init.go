package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MatchaScript/nanokube/internal/config"
	"github.com/MatchaScript/nanokube/internal/initialize"
	"github.com/MatchaScript/nanokube/internal/state"
	"github.com/MatchaScript/nanokube/internal/version"
)

// newInitCmd is the one-time initialisation verb operators run on a
// fresh node. It mirrors `kubeadm init`'s scope: render PKI / kubeconfigs
// / static pod manifests, start kubelet, wait for the apiserver, seed
// the cluster-admins CRB, mark the node, install addons. On success the
// cluster is healthy on this host and the operator's next step is
// `systemctl enable nanokube.service` so future reboots run under
// supervisor control. Refuses to run on a node that already has nanokube
// state; operators must `nanokube reset --yes` first to start over.
func newInitCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialise a fresh node (run once per install)",
		Long: "Mirrors `kubeadm init`'s scope: writes PKI, kubeconfigs, " +
			"static pod manifests, and kubelet config; starts kubelet; " +
			"waits for the apiserver; seeds the cluster-admins " +
			"ClusterRoleBinding; marks the control-plane node; installs " +
			"addons. After this completes, enable nanokube.service so " +
			"subsequent boots reconcile automatically. Refuses to run if " +
			"nanokube state already exists on this node; run " +
			"`nanokube reset --yes` first to re-init.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			existed, err := state.Exists(g.layout)
			if err != nil {
				return err
			}
			if existed {
				return errors.New("nanokube state already exists; run `nanokube reset --yes` first to re-initialise")
			}
			loaded, err := config.Load(g.configPath, g.layout)
			if err != nil {
				return err
			}
			if loaded.HasJoin {
				return fmt.Errorf("config %s describes a joined node (JoinConfiguration); `nanokube init` bootstraps a new cluster — use `nanokube add-node`", g.configPath)
			}
			return initialize.Run(cmd.Context(), loaded.Init, g.layout, version.KubernetesVersion, cmd.OutOrStdout())
		},
	}
}

// defaultNodeName matches kubeadm/kubelet: lowercased OS hostname.
func defaultNodeName() (string, error) {
	h, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("get hostname: %w", err)
	}
	return strings.ToLower(h), nil
}
