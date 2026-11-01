package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/scenario"
	"github.com/nourtalaat/sidekick/internal/scrub"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/summarize"
)

// exportConns is the size of the pool `bench export` uses: a transaction at
// a time.
const exportConns = 2

// exportActorID names the command line in the audit log.
const exportActorID = "cli"

// exportFlags are the flags of `sidekick bench export`.
type exportFlags struct {
	configPath    string
	investigation string
	scenario      string
	out           string
	force         bool
}

// newBenchExportCmd builds `sidekick bench export`.
func newBenchExportCmd(regs config.Registries) *cobra.Command {
	var flags exportFlags

	cmd := &cobra.Command{
		Use:   "export (--investigation <id> | --scenario <id> --out <file>)",
		Short: "Export a confirmed investigation as a scenario draft, or write out a reviewed scenario",
		Long: `Turn a confirmed investigation into a benchmark scenario, and write a
reviewed scenario to a file.

--investigation <id> builds a draft of the investigation in the database of
the config file's database.urlSecret and prints the scenario's ID. The draft
holds the alert's payload, one fixture for each source call, the confirmed
verdict and cause, and rubric items drafted by the model of
model.roles.summarize when there is one. Emails, user names, account IDs,
host names and IP addresses are replaced by placeholders such as host-1.
The draft cannot be run or written out until an admin has reviewed it in the
web UI, on the scenarios tab of the review queue.

--scenario <id> --out <file> writes a reviewed scenario to a file that
bench run and bench validate read. A draft is refused. The file names the
tool snapshots of its sources, such as bench/tools/signoz.json; the command
warns about those that are not in the current directory.

Exit status: 0 on success, 1 for a draft, an investigation that cannot be
exported or any other failure, 2 for bad flags, a bad config file or an ID
that does not exist, 3 when the database cannot be reached.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return benchExport(cmd.Context(), cmd, regs, flags)
		},
	}

	addConfigFlag(cmd, &flags.configPath)
	cmd.Flags().StringVar(&flags.investigation, "investigation", "", "the ID of the confirmed investigation to export")
	cmd.Flags().StringVar(&flags.scenario, "scenario", "", "the ID of the reviewed scenario to write out")
	cmd.Flags().StringVarP(&flags.out, "out", "o", "", "the file to write the scenario to (with --scenario)")
	cmd.Flags().BoolVar(&flags.force, "force", false, "overwrite --out when it exists")
	cmd.MarkFlagsOneRequired("investigation", "scenario")
	cmd.MarkFlagsMutuallyExclusive("investigation", "scenario")

	return cmd
}

// benchExport checks the flags and runs the mode they name.
func benchExport(ctx context.Context, cmd *cobra.Command, regs config.Registries, flags exportFlags) error {
	if (flags.scenario != "") != (flags.out != "") {
		return &exitError{code: exitBadInput, err: errors.New("--out goes with --scenario and with nothing else")}
	}

	var id uuid.UUID

	if flags.investigation != "" {
		var err error

		if id, err = uuid.Parse(flags.investigation); err != nil {
			return &exitError{code: exitBadInput, err: fmt.Errorf("--investigation must be an investigation ID: %w", err)}
		}
	}

	cfg, err := config.Load(ctx, flags.configPath, config.LoadOptions{Registries: regs})
	if err != nil {
		return configProblems(cmd, err)
	}

	pool, err := exportPool(ctx, cfg)
	if err != nil {
		return err
	}

	defer pool.Close()

	actor := scenario.Actor{Kind: audit.ActorSystem, ID: exportActorID}

	if flags.scenario != "" {
		return writeScenario(ctx, cmd, scenario.NewService(scenario.Options{Pool: pool, Audit: audit.NewWriter()}), flags, actor)
	}

	service, err := exportService(ctx, cmd, regs, cfg, pool)
	if err != nil {
		return err
	}

	return exportInvestigation(ctx, cmd, service, id, actor)
}

// exportInvestigation builds the draft and prints its ID, and what the
// exporter noticed on stderr.
func exportInvestigation(ctx context.Context, cmd *cobra.Command, service *scenario.Service, id uuid.UUID, actor scenario.Actor) error {
	row, created, err := service.Export(ctx, id, actor)
	if err != nil {
		return exportError(err)
	}

	detail, err := service.Detail(ctx, row.ID)
	if err != nil {
		return fmt.Errorf("read the new draft: %w", err)
	}

	if !created {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "the investigation has a scenario already (%s)\n", row.Status); err != nil {
			return fmt.Errorf("print the status: %w", err)
		}
	}

	for _, n := range detail.Notes {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "note: "+n.Text); err != nil {
			return fmt.Errorf("print a note: %w", err)
		}
	}

	if _, err := fmt.Fprintln(cmd.OutOrStdout(), row.ID); err != nil {
		return fmt.Errorf("print the scenario ID: %w", err)
	}

	return nil
}

// writeScenario writes a reviewed scenario to --out.
func writeScenario(ctx context.Context, cmd *cobra.Command, service *scenario.Service, flags exportFlags, actor scenario.Actor) error {
	if _, err := os.Stat(flags.out); err == nil && !flags.force {
		return &exitError{code: exitBadInput, err: fmt.Errorf("%s exists; use --force to overwrite it", flags.out)}
	}

	body, err := service.Download(ctx, flags.scenario, actor)
	if err != nil {
		return exportError(err)
	}

	flag := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if flags.force {
		flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}

	file, err := os.OpenFile(flags.out, flag, 0o600) //nolint:gosec // The user chooses the output file.
	if errors.Is(err, fs.ErrExist) {
		return &exitError{code: exitBadInput, err: fmt.Errorf("%s exists; use --force to overwrite it", flags.out)}
	}

	if err != nil {
		return &exitError{code: exitBadInput, err: fmt.Errorf("open --out: %w", err)}
	}

	if _, err := file.Write(body); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", flags.out, err), file.Close())
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", flags.out, err)
	}

	warnMissingSnapshots(cmd, body)

	if _, err := fmt.Fprintln(cmd.OutOrStdout(), flags.out); err != nil {
		return fmt.Errorf("print the file name: %w", err)
	}

	return nil
}

// warnMissingSnapshots tells which tool snapshots the scenario names that
// are not where bench run will look for them.
func warnMissingSnapshots(cmd *cobra.Command, body []byte) {
	sc, err := bench.ParseScenario(body)
	if err != nil {
		return
	}

	for _, name := range sc.SourceNames() {
		snapshot := sc.Sources[name].Tools.Snapshot
		if snapshot == "" {
			continue
		}

		if _, err := os.Stat(filepath.Clean(snapshot)); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: source %s reads its tools from %s, which is not here; add it before bench run\n", name, snapshot)
		}
	}
}

// exportError turns an error of the scenario service into the exit code
// the command gives.
func exportError(err error) error {
	if errors.Is(err, scenario.ErrInvestigationNotFound) || errors.Is(err, scenario.ErrNotFound) {
		return &exitError{code: exitBadInput, err: err}
	}

	if errors.Is(err, scenario.ErrNotReviewed) {
		return fmt.Errorf("%w; an admin reviews drafts in the web UI", err)
	}

	return fmt.Errorf("export: %w", err)
}

// exportPool opens the database of the config, which must have every
// migration of this Sidekick.
func exportPool(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	url, err := cfg.DatabaseURL(ctx, nil)
	if err != nil {
		return nil, &exitError{code: exitBadInput, err: fmt.Errorf("database.urlSecret cannot be read: %w", err)}
	}

	pool, err := store.Open(ctx, url, exportConns)
	if err != nil {
		return nil, &exitError{code: exitInfrastructure, err: fmt.Errorf("connect to the database: %w", err)}
	}

	current, target, err := store.SchemaVersion(ctx, pool)
	if err != nil {
		pool.Close()

		return nil, &exitError{code: exitInfrastructure, err: fmt.Errorf("read the database's schema version: %w", err)}
	}

	if current < target {
		pool.Close()

		return nil, &exitError{code: exitBadInput, err: fmt.Errorf("the database's schema is at version %d and this Sidekick needs %d; run sidekick migrate first", current, target)}
	}

	return pool, nil
}

// exportService builds the scenario service of the command: the cleaner of
// the config's redaction settings and, when model.roles.summarize is set,
// the rubric drafter over that connector.
func exportService(ctx context.Context, cmd *cobra.Command, regs config.Registries, cfg *config.Config, pool *pgxpool.Pool) (*scenario.Service, error) {
	log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	deps := registry.Deps{Secrets: secrets.NewDir(cfg.Secrets.Dir), HTTP: httpclient.New(), Logger: log}

	redaction, err := cfg.Redaction.RedactOptions(ctx, deps.Secrets)
	if err != nil {
		return nil, &exitError{code: exitBadInput, err: fmt.Errorf("read the redaction settings: %w", err)}
	}

	cleaner, err := scrub.New(scrub.Options{HashKey: redaction.HashKey, Custom: redaction.Custom, Logger: log})
	if err != nil {
		return nil, &exitError{code: exitBadInput, err: fmt.Errorf("set up the cleaner: %w", err)}
	}

	caller, err := exportSummarizer(ctx, cmd, regs, cfg, deps)
	if err != nil {
		return nil, err
	}

	if !caller.Enabled() {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "model.roles.summarize is not set, so the draft has no rubric items; write them in the web UI"); err != nil {
			return nil, fmt.Errorf("print a note: %w", err)
		}
	}

	return scenario.NewService(scenario.Options{
		Pool: pool, Cleaner: cleaner, Rubric: scenario.NewRubricDrafter(caller), Audit: audit.NewWriter(),
		Webhooks: cfg.Intake.Webhooks, Sources: cfg.Sources,
	}), nil
}

// exportSummarizer builds the connector of the summarize role, and no other
// plugin of the config. Without the role the caller is off.
func exportSummarizer(ctx context.Context, cmd *cobra.Command, regs config.Registries, cfg *config.Config, deps registry.Deps) (*summarize.Caller, error) {
	name := cfg.Model.Roles[model.RoleSummarize]
	if name == "" {
		return summarize.New(summarize.Options{}), nil
	}

	modelsOnly := *cfg
	modelsOnly.Sources = nil
	modelsOnly.Outputs = nil
	modelsOnly.Intake = config.Intake{}
	modelsOnly.Routes = nil
	modelsOnly.Memory.Vector.Enabled = false

	built, err := config.Build(ctx, &modelsOnly, regs, deps)
	if err != nil {
		return nil, configProblems(cmd, err)
	}

	conn, ok := built.Models[name]
	if !ok {
		return nil, &exitError{code: exitBadInput, err: fmt.Errorf("model.roles.summarize names %q, which is not a model connector", name)}
	}

	opts, err := summarizeOptions(cfg, name, conn, built.ModelSettings[name], pricing.New(cfg.Model.PriceTable()))
	if err != nil {
		return nil, &exitError{code: exitBadInput, err: err}
	}

	opts.Logger = deps.Logger

	return summarize.New(opts), nil
}
