package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/config"
	_ "github.com/nourtalaat/sidekick/internal/plugins" // Registers the built-in plugin types.
	"github.com/nourtalaat/sidekick/internal/registry"
)

// errInvalidConfig is returned after the problems in a config file have been
// printed, so the process exits with status 1 without printing them again.
var errInvalidConfig = errors.New("config is not valid")

// newConfigCmd builds `sidekick config`, which holds the config subcommands.
// regs are the plugin registries; the zero value means the global ones.
func newConfigCmd(regs config.Registries) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Work with the sidekick.yaml config file",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(newConfigValidateCmd(regs))

	return cmd
}

// newConfigValidateCmd builds `sidekick config validate`, which runs the
// start-up checks on a config file and prints every problem, one per line.
func newConfigValidateCmd(regs config.Registries) *cobra.Command {
	var (
		file         string
		checkSecrets bool
	)

	cmd := &cobra.Command{
		Use:   "validate -f <file>",
		Short: "Check a config file and print every problem in it",
		Long: `Check a config file and print every problem in it, one per line, each
with the path of the field it is about. The exit status is 0 when the file
is valid and 1 when it is not.

By default no secret file is read and no plugin instance is built, so the
check runs in CI without mounted secrets and makes no network calls. With
--check-secrets it also checks that every referenced secret file exists and
is readable, and builds every instance, which are the checks start-up runs.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := validateConfig(cmd, regs, file, checkSecrets)
			if err == nil {
				return writeLine(cmd.OutOrStdout(), file+" is valid")
			}

			cmd.SilenceErrors = true

			if werr := printProblems(cmd.ErrOrStderr(), err); werr != nil {
				return errors.Join(errInvalidConfig, werr)
			}

			return errInvalidConfig
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "the config file to check (required)")
	cmd.Flags().BoolVar(&checkSecrets, "check-secrets", false, "also read every secret file and build every instance, as start-up does")

	if err := cmd.MarkFlagRequired("file"); err != nil {
		panic(fmt.Sprintf("mark flag required: %v", err))
	}

	return cmd
}

// validateConfig loads and checks the file, and with checkSecrets also
// builds every instance and closes them again.
func validateConfig(cmd *cobra.Command, regs config.Registries, file string, checkSecrets bool) error {
	ctx := cmd.Context()

	cfg, err := config.Load(ctx, file, config.LoadOptions{Registries: regs, CheckSecrets: checkSecrets})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if !checkSecrets {
		return nil
	}

	deps := registry.Deps{HTTP: http.DefaultClient, Logger: slog.New(slog.DiscardHandler)}

	instances, err := config.Build(ctx, cfg, regs, deps)
	if err != nil {
		return fmt.Errorf("build config: %w", err)
	}

	if err := instances.Close(); err != nil {
		return fmt.Errorf("close instances: %w", err)
	}

	return nil
}

// printProblems writes each problem in err on its own line.
func printProblems(w io.Writer, err error) error {
	var problems config.Errors
	if !errors.As(err, &problems) {
		return writeLine(w, err.Error())
	}

	for _, p := range problems {
		if err := writeLine(w, p.String()); err != nil {
			return fmt.Errorf("print problem: %w", err)
		}
	}

	return nil
}

func writeLine(w io.Writer, line string) error {
	if _, err := fmt.Fprintln(w, line); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
