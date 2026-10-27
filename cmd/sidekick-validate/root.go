package main

import (
	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
)

// newRootCmd builds the sidekick-validate command tree. Each subcommand
// lives in its own file and is added here.
func newRootCmd(info buildinfo.Info) *cobra.Command {
	root := &cobra.Command{
		Use:          "sidekick-validate",
		Short:        "Check monitor definition files before Sidekick opens a pull request",
		SilenceUsage: true,
	}

	root.AddCommand(newServeCmd())
	root.AddCommand(newJobCmd())
	root.AddCommand(newHealthCmd())
	root.AddCommand(newVersionCmd(info))

	return root
}
