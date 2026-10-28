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

// compareArgs is the number of reports `sidekick bench compare` takes.
const compareArgs = 2

// defaultJudgeCases is the directory of the hand-graded reports that
// `sidekick bench judge-check` reads by default.
const defaultJudgeCases = "bench/judge-check"

// validateFlags are the flags of `sidekick bench validate`.
type validateFlags struct {
	configPath string
	scenarios  string
}

// judgeCheckFlags are the flags of `sidekick bench judge-check`.
type judgeCheckFlags struct {
	configPath   string
	cases        string
	minAgreement float64
}

// benchFlags are the flags of `sidekick bench run`.
type benchFlags struct {
	configPath string
	scenarios  string
	repeat     int
	db         bool
	// settings is bench.yaml.
	settings string
	seed     uint64
	label    string
	out      string
	junit    string
	markdown string
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
	cmd.AddCommand(newBenchValidateCmd(regs))
	cmd.AddCommand(newBenchJudgeCheckCmd(regs))
	cmd.AddCommand(newBenchCompareCmd())

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
database never sees the runs' jobs. The scored results are then stored in
bench_runs and bench_results of the database's default schema, which must
have every migration (run sidekick migrate first). The database role needs
CREATE on the database and INSERT on those two tables.

Once every run has finished, the judge grades each run's root cause, the
runs are scored and the command prints the spread of each scenario, the
run totals with their 95% intervals, and the thresholds of --bench-config.
--out, --junit and --markdown write the results to files as well.

Exit status: 0 when every run finished and every threshold is met, 1 when
a threshold failed, 2 for bad flags, a bad config, bench.yaml, baseline or
scenario file, 3 when the model provider is unreachable after its retries
or Docker or the database fails, and 1 for anything else.`,
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
	cmd.Flags().BoolVar(&flags.db, "db", false, "run in a schema of its own on the database of database.urlSecret, instead of a temporary Postgres, and store the results there")
	cmd.Flags().StringVar(&flags.settings, "bench-config", "", "a bench.yaml with the weights of the overall score and the thresholds")
	cmd.Flags().Uint64Var(&flags.seed, "seed", bench.DefaultSeed, "seeds the resampling of the 95% intervals")
	cmd.Flags().StringVar(&flags.label, "label", "", "a name for the run, kept in report.json")
	cmd.Flags().StringVar(&flags.out, "out", "", "write the results to this report.json")
	cmd.Flags().StringVar(&flags.junit, "junit", "", "write the results to this JUnit XML file")
	cmd.Flags().StringVar(&flags.markdown, "markdown", "", "write a Markdown summary, for a pull request comment, to this file")

	if err := cmd.MarkFlagRequired("scenarios"); err != nil {
		panic(fmt.Sprintf("mark flag required: %v", err))
	}

	return cmd
}

// newBenchValidateCmd builds `sidekick bench validate`.
func newBenchValidateCmd(regs config.Registries) *cobra.Command {
	var flags validateFlags

	cmd := &cobra.Command{
		Use:   "validate --scenarios <file or directory>",
		Short: "Check benchmark scenario files without running them",
		Long: `Check each benchmark scenario the way bench run checks them before it runs
anything: the file format, the strategy, the intake adapter and its payload,
and each mock source with its tool snapshot and fixtures. It prints every
problem with its file and field.

It calls no model, and needs neither Postgres nor Docker. The config file
is read for its strategies and its model connectors, so it needs the
secrets the connectors read, but nothing is sent to a provider.

Exit status: 0 when every scenario is valid, 2 for bad flags, a bad config
file or a bad scenario file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return benchValidate(cmd, regs, flags)
		},
	}

	addConfigFlag(cmd, &flags.configPath)
	cmd.Flags().StringVarP(&flags.scenarios, "scenarios", "s", "", "a scenario file, or a directory of them (required)")

	if err := cmd.MarkFlagRequired("scenarios"); err != nil {
		panic(fmt.Sprintf("mark flag required: %v", err))
	}

	return cmd
}

// newBenchJudgeCheckCmd builds `sidekick bench judge-check`.
func newBenchJudgeCheckCmd(regs config.Registries) *cobra.Command {
	var flags judgeCheckFlags

	cmd := &cobra.Command{
		Use:   "judge-check [--cases bench/judge-check]",
		Short: "Grade hand-graded reports with the judge and show how far its grades agree",
		Long: `Have the judge grade each hand-graded report and compare its grades with
the hand grades, item by item, to see how far a judge model's grades move.

The judge is the model of the config file's model.roles.judge, or of
model.roles.investigate when no judge role is set.

Exit status: 0 when the judge graded every case and agrees with at least
--min-agreement of the items, 1 when it agrees with fewer, 2 for bad
flags, a bad config file or a bad case file, and 3 when the model provider
is unreachable after its retries.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return benchJudgeCheck(cmd.Context(), cmd, regs, flags)
		},
	}

	addConfigFlag(cmd, &flags.configPath)
	cmd.Flags().StringVar(&flags.cases, "cases", defaultJudgeCases, "a case file, or a directory of them")
	cmd.Flags().Float64Var(&flags.minAgreement, "min-agreement", 0, "the share of items, from 0 to 1, the judge must grade as the hand grades do")

	return cmd
}

// newBenchCompareCmd builds `sidekick bench compare`.
func newBenchCompareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compare <a.json> <b.json>",
		Short: "Show two benchmark reports side by side",
		Long: `Show two report.json files of sidekick bench run side by side: what each
run used, each metric with the change from the first to the second, and
each scenario's runs. A share whose 95% intervals overlap in the two runs
is flagged "no clear difference": the change may be chance.

Exit status: 0 when both reports were read, 2 when one cannot be read or
has another schema version.`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != compareArgs {
				return &exitError{code: exitBadInput, err: fmt.Errorf("compare takes two report files, not %d", len(args))}
			}

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return benchCompare(cmd, args[0], args[1])
		},
	}
}

// benchRun checks the flags, the settings, the scenarios and the config,
// gets Postgres, runs the scenarios, scores them and prints the results,
// writes the output files and checks the thresholds. When a run fails it
// prints the table of the runs that finished before it, unscored.
func benchRun(ctx context.Context, cmd *cobra.Command, regs config.Registries, flags benchFlags) (err error) {
	if flags.repeat < 1 {
		return &exitError{code: exitBadInput, err: fmt.Errorf("--repeat must be at least 1, not %d", flags.repeat)}
	}

	settings, baseline, err := benchSettings(flags.settings)
	if err != nil {
		return &exitError{code: exitBadInput, err: err}
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

	judge, err := runner.Judge()
	if err != nil {
		return &exitError{code: exitBadInput, err: err}
	}

	var (
		url         string
		resultStore *bench.ResultStore
	)

	if flags.db {
		url, resultStore, err = benchResultStore(ctx, cfg)
		if err != nil {
			return err
		}

		defer resultStore.Close()
	}

	database, err := benchDatabase(ctx, url, log)
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
	if runErr == nil {
		log.InfoContext(ctx, "grading the root causes", "judge", judge.Name())

		runErr = bench.Score(ctx, results, bench.ScoreOptions{Grader: judge, Weights: settings.ScoreWeights(), Seed: flags.seed})
	}

	if err := results.WriteTable(cmd.OutOrStdout()); err != nil {
		return errors.Join(runErr, fmt.Errorf("print the results: %w", err))
	}

	switch {
	case runErr == nil:
	case errors.Is(runErr, bench.ErrInfrastructure):
		return &exitError{code: exitInfrastructure, err: runErr}
	default:
		return fmt.Errorf("run the scenarios: %w", runErr)
	}

	thresholds := settings.Thresholds.Check(*results.Summary, baseline)

	if err := printBenchResults(cmd, results, thresholds); err != nil {
		return err
	}

	info := benchInfo(runner, flags)

	if err := writeBenchOutputs(results, info, thresholds, flags); err != nil {
		return err
	}

	if resultStore != nil {
		if err := storeBenchResults(ctx, resultStore, results, info, thresholds, log); err != nil {
			return err
		}
	}

	if failed := bench.Failed(thresholds); len(failed) > 0 {
		return &exitError{code: exitThresholdFailed, err: fmt.Errorf("%d of %d thresholds failed: %s", len(failed), len(thresholds), thresholdNames(failed))}
	}

	return nil
}

// benchValidate loads the scenarios and the config and runs the checks that
// bench run makes before its first run, without a database or a model call.
func benchValidate(cmd *cobra.Command, regs config.Registries, flags validateFlags) error {
	ctx := cmd.Context()

	scenarios, err := bench.LoadScenarios(flags.scenarios, bench.LoadOptions{})
	if err != nil {
		return &exitError{code: exitBadInput, err: fmt.Errorf("load the scenarios: %w", err)}
	}

	cfg, err := config.Load(ctx, flags.configPath, config.LoadOptions{Registries: regs})
	if err != nil {
		return configProblems(cmd, err)
	}

	if _, err := bench.New(ctx, scenarios, bench.Options{Config: cfg, Registries: regs}); err != nil {
		if errors.Is(err, bench.ErrInvalidScenario) {
			return &exitError{code: exitBadInput, err: err}
		}

		return configProblems(cmd, err)
	}

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "scenarios checked: %d, problems: 0\n", len(scenarios)); err != nil {
		return fmt.Errorf("print the result: %w", err)
	}

	return nil
}

// benchJudgeCheck loads the cases and the config, has the judge grade each
// case and prints how far its grades agree with the hand grades.
func benchJudgeCheck(ctx context.Context, cmd *cobra.Command, regs config.Registries, flags judgeCheckFlags) error {
	if flags.minAgreement < 0 || flags.minAgreement > 1 {
		return &exitError{code: exitBadInput, err: fmt.Errorf("--min-agreement must be from 0 to 1, not %g", flags.minAgreement)}
	}

	cases, err := bench.LoadJudgeCases(flags.cases)
	if err != nil {
		return &exitError{code: exitBadInput, err: fmt.Errorf("load the judge-check cases: %w", err)}
	}

	cfg, err := config.Load(ctx, flags.configPath, config.LoadOptions{Registries: regs})
	if err != nil {
		return configProblems(cmd, err)
	}

	log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelInfo}))

	runner, err := bench.New(ctx, nil, bench.Options{Config: cfg, Registries: regs, Logger: log})
	if err != nil {
		return configProblems(cmd, err)
	}

	judge, err := runner.Judge()
	if err != nil {
		return &exitError{code: exitBadInput, err: err}
	}

	log.InfoContext(ctx, "grading the judge-check cases", "cases", len(cases), "judge", judge.Name())

	check, err := bench.CheckJudge(ctx, judge, cases)
	if err != nil {
		if errors.Is(err, bench.ErrInfrastructure) {
			return &exitError{code: exitInfrastructure, err: err}
		}

		return fmt.Errorf("check the judge: %w", err)
	}

	if err := check.WriteTable(cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("print the judge-check results: %w", err)
	}

	if flags.minAgreement > 0 && (check.Agreement == nil || *check.Agreement < flags.minAgreement) {
		return &exitError{code: exitThresholdFailed, err: fmt.Errorf("the judge agrees with fewer than %g of the hand grades", flags.minAgreement)}
	}

	return nil
}

// benchCompare reads two reports and prints them side by side.
func benchCompare(cmd *cobra.Command, pathA, pathB string) error {
	a, err := bench.ReadReport(pathA)
	if err != nil {
		return &exitError{code: exitBadInput, err: fmt.Errorf("read the first report: %w", err)}
	}

	b, err := bench.ReadReport(pathB)
	if err != nil {
		return &exitError{code: exitBadInput, err: fmt.Errorf("read the second report: %w", err)}
	}

	if err := bench.WriteComparison(cmd.OutOrStdout(), a, b); err != nil {
		return fmt.Errorf("print the comparison: %w", err)
	}

	return nil
}

// benchResultStore reads the URL of the config's database for --db and
// opens the store of the results in its default schema, which must have
// every migration of this Sidekick.
func benchResultStore(ctx context.Context, cfg *config.Config) (string, *bench.ResultStore, error) {
	url, err := cfg.DatabaseURL(ctx, nil)
	if err != nil {
		return "", nil, &exitError{code: exitBadInput, err: fmt.Errorf("--db uses the database of database.urlSecret, which cannot be read: %w", err)}
	}

	resultStore, err := bench.OpenResultStore(ctx, url)
	if err != nil {
		code := exitInfrastructure
		if errors.Is(err, bench.ErrSchemaNotCurrent) {
			code = exitBadInput
		}

		return "", nil, &exitError{code: code, err: fmt.Errorf("--db: %w", err)}
	}

	return url, resultStore, nil
}

// benchDatabase starts a temporary Postgres, or, with a url from --db,
// opens a schema of the benchmark's own on that database.
func benchDatabase(ctx context.Context, url string, log *slog.Logger) (*bench.Database, error) {
	if url == "" {
		database, err := bench.StartTemporary(ctx, log)
		if err != nil {
			return nil, &exitError{code: exitInfrastructure, err: err}
		}

		return database, nil
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
