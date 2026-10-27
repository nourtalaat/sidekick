package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/doctor"
)

// errDoctorFailed is returned after doctor printed a failed check, so the
// process exits with status 1 without printing more.
var errDoctorFailed = errors.New("a check failed")

// newDoctorCmd builds `sidekick doctor`, which checks everything the config
// names and prints one line per check, with a fix for each problem. regs
// are the plugin registries; the zero value means the global ones.
func newDoctorCmd(regs config.Registries) *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the database, Slack, the model keys and every source, and print fixes",
		Long: `Check that Sidekick can do its work with the config file: that the
database answers and has every migration this release needs, that Slack
accepts the bot token (auth.test), that each model connector's API key
works (with a call that costs nothing, such as listing models), and that
each source connects and lists its tools, with the tools the model will
not see and why. It also warns about models that have no price, about
investigate connectors that cannot be forced to call a tool, about
local model presets without a server set, and about a preset that Ollama
runs with too small a context.

It prints one line per check, with a fix below each problem, and never
prints a secret. The exit status is 1 when any check fails. Warnings do
not change it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Context(), configPath, config.LoadOptions{Registries: regs})
			if err != nil {
				cmd.SilenceErrors = true

				if werr := printProblems(cmd.ErrOrStderr(), err); werr != nil {
					return errors.Join(errInvalidConfig, werr)
				}

				return errInvalidConfig
			}

			results := doctor.Run(cmd.Context(), doctor.Options{Config: cfg, Registries: regs})
			if err := doctor.Write(cmd.OutOrStdout(), results); err != nil {
				return fmt.Errorf("print results: %w", err)
			}

			if doctor.Failed(results) {
				cmd.SilenceErrors = true

				return errDoctorFailed
			}

			return nil
		},
	}

	addConfigFlag(cmd, &configPath)

	return cmd
}
