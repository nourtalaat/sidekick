package main

import (
	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/config"
)

// newRootCmd builds the sidekick command tree. Each subcommand lives in its
// own file and is added here.
func newRootCmd(info buildinfo.Info) *cobra.Command {
	root := &cobra.Command{
		Use:          "sidekick",
		Short:        "Investigate alerts and report whether they are real",
		SilenceUsage: true,
	}

	root.AddCommand(newVersionCmd(info))
	root.AddCommand(newMigrateCmd(config.Registries{}))
	root.AddCommand(newConfigCmd(config.Registries{}))
	root.AddCommand(newServeCmd(config.Registries{}, info))
	root.AddCommand(newDoctorCmd(config.Registries{}))
	root.AddCommand(newTestAlertCmd(config.Registries{}))

	return root
}
