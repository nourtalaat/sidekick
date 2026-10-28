package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
)

// newVersionCmd builds `sidekick-validate version`, which prints the
// version and commit set at build time.
func newVersionCmd(info buildinfo.Info) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and commit of this build",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "sidekick-validate %s (commit %s)\n", info.Version, info.Commit); err != nil {
				return fmt.Errorf("write version: %w", err)
			}

			return nil
		},
	}
}
