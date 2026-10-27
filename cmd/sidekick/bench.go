package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/config"
)

// benchCloseTimeout bounds stopping the temporary Postgres or dropping the
// benchmark's schema, which runs even after Ctrl-C.
const benchCloseTimeout = time.Minute

// benchFlags are the flags of `sidekick bench run`.
type benchFlags struct {
	configPath string
	scenarios  string
	repeat     int
	db         bool
}

// newBenchCmd builds `sidekick bench`, which holds the benchmark
// subcommands. regs are the plugin registries; the zero value means the
// global ones.
func newBenchCmd(regs config.Registries) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Run benchmark scenarios against mock sources",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(newBenchRunCmd(regs))

	return cmd
}

// newBenchRunCmd builds `sidekick bench run`.
func newBenchRunCmd(regs config.Registries) *cobra.Command {
	var flags benchFlags

	cmd := &cobra.Command{
		Use:   "run --scenarios <file or directory>",
		Short: "Run benchmark scenarios and print a table of the results",
		Long: `Run each benchmark scenario --repeat times and print one line per run.

Each run takes the path an alert takes in serve: the scenario's payload goes
through its intake adapter, the router opens an investigation, and the
investigation runs with the scenario's mock sources, clock and limits and
the model connectors of the config file.

Runs need Postgres. By default a temporary Postgres is started in a
container with Docker, and removed when the runs end, Ctrl-C included.
With --db the runs use the database of the config file's
database.urlSecret instead, in a schema of their own that is dropped at the
end, so nothing is left behind and a Sidekick serving from the same
database never sees the runs' jobs. The database role then needs CREATE on
the database.

Exit status: 0 when every run finished, 2 for bad flags, a bad config file
or a bad scenario file, 3 when the model provider is unreachable after its
retries or Docker or the database fails, and 1 for anything else.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()

			// Once the first signal stops the runs, the next one gets the
			// default behaviour, which ends the process.
			context.AfterFunc(ctx, stop)

			return benchRun(ctx, cmd, regs, flags)
		},
	}

	addConfigFlag(cmd, &flags.configPath)
	cmd.Flags().StringVarP(&flags.scenarios, "scenarios", "s", "", "a scenario file, or a directory of them (required)")
	cmd.Flags().IntVarP(&flags.repeat, "repeat", "n", bench.DefaultRepeat, "how many times each scenario runs")
	cmd.Flags().BoolVar(&flags.db, "db", false, "run in a schema of its own on the database of database.urlSecret, instead of a temporary Postgres")

	if err := cmd.MarkFlagRequired("scenarios"); err != nil {
		panic(fmt.Sprintf("mark flag required: %v", err))
	}

	return cmd
}

// benchRun checks the flags, the scenarios and the config, gets Postgres,
// runs the scenarios and prints the table of results, also of the runs
// that finished before a failure or Ctrl-C.
func benchRun(ctx context.Context, cmd *cobra.Command, regs config.Registries, flags benchFlags) (err error) {
	if flags.repeat < 1 {
		return &exitError{code: exitBadInput, err: fmt.Errorf("--repeat must be at least 1, not %d", flags.repeat)}
	}

	scenarios, err := bench.LoadScenarios(flags.scenarios, bench.LoadOptions{})
	if err != nil {
		return &exitError{code: exitBadInput, err: fmt.Errorf("load the scenarios: %w", err)}
	}

	cfg, err := config.Load(ctx, flags.configPath, config.LoadOptions{Registries: regs})
	if err != nil {
		return configProblems(cmd, err)
	}

	log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelInfo}))

	runner, err := bench.New(ctx, scenarios, bench.Options{
		Config:          cfg,
		Registries:      regs,
		Repeat:          flags.repeat,
		Logger:          log,
		ComponentLogger: slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		if errors.Is(err, bench.ErrInvalidScenario) {
			return &exitError{code: exitBadInput, err: err}
		}

		return configProblems(cmd, err)
	}

	database, err := benchDatabase(ctx, cfg, flags.db, log)
	if err != nil {
		return err
	}

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), benchCloseTimeout)
		defer cancel()

		if cerr := database.Close(closeCtx); cerr != nil {
			err = errors.Join(err, fmt.Errorf("clean up: %w", cerr))
		}
	}()

	results, runErr := runner.Run(ctx, database)

	if err := results.WriteTable(cmd.OutOrStdout()); err != nil {
		return errors.Join(runErr, fmt.Errorf("print the results: %w", err))
	}

	switch {
	case runErr == nil:
		return nil
	case errors.Is(runErr, bench.ErrInfrastructure):
		return &exitError{code: exitInfrastructure, err: runErr}
	default:
		return fmt.Errorf("run the scenarios: %w", runErr)
	}
}

// benchDatabase starts a temporary Postgres, or, with db, opens a schema of
// the benchmark's own on the config's database.
func benchDatabase(ctx context.Context, cfg *config.Config, db bool, log *slog.Logger) (*bench.Database, error) {
	if !db {
		database, err := bench.StartTemporary(ctx, log)
		if err != nil {
			return nil, &exitError{code: exitInfrastructure, err: err}
		}

		return database, nil
	}

	url, err := cfg.DatabaseURL(ctx, nil)
	if err != nil {
		return nil, &exitError{code: exitBadInput, err: fmt.Errorf("--db uses the database of database.urlSecret, which cannot be read: %w", err)}
	}

	database, err := bench.OpenSchema(ctx, url, log)
	if err != nil {
		code := exitInfrastructure
		if errors.Is(err, bench.ErrCannotCreateSchema) {
			code = exitBadInput
		}

		return nil, &exitError{code: code, err: err}
	}

	return database, nil
}

// configProblems prints the problems of a config file, one per line, as
// config validate prints them, and returns an error with exitBadInput. An
// error that is not about the config file is returned as it is.
func configProblems(cmd *cobra.Command, err error) error {
	var problems config.Errors
	if !errors.As(err, &problems) {
		return fmt.Errorf("check the config: %w", err)
	}

	cmd.SilenceErrors = true

	if werr := printProblems(cmd.ErrOrStderr(), err); werr != nil {
		return &exitError{code: exitBadInput, err: errors.Join(errInvalidConfig, werr)}
	}

	return &exitError{code: exitBadInput, err: errInvalidConfig}
}
