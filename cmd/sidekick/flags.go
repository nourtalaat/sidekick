package main

import "github.com/spf13/cobra"

// defaultConfigPath is where the Helm chart mounts sidekick.yaml.
const defaultConfigPath = "/etc/sidekick/sidekick.yaml"

// addConfigFlag adds --config (-c), the path of sidekick.yaml, to cmd and
// its subcommands.
func addConfigFlag(cmd *cobra.Command, path *string) {
	cmd.PersistentFlags().StringVarP(path, "config", "c", defaultConfigPath, "the sidekick.yaml config file")
}
