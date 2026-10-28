package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/store"
)

const (
	// migrateMaxConns is the pool size for the migrate commands. goose
	// holds one connection for its lock and runs the migrations on it; the
	// second means goose never waits on itself for a connection.
	migrateMaxConns = 2

	tabMinWidth = 0
	tabWidth    = 8
	tabPadding  = 2
	tabPadChar  = ' '
	tabFlags    = 0
)

// Audit entry of `sidekick migrate vector-reset`.
const (
	auditVectorsReset = "memory.vectors_reset"
	auditVectorsTable = "memory_embeddings"
)

// errResetDeclined is returned when the answer to the question of
// `sidekick migrate vector-reset` is not yes.
var errResetDeclined = errors.New("vector memory was not reset")

// errVectorsOff is returned by `sidekick migrate vector-reset` when the
// config does not turn on vector memory.
var errVectorsOff = errors.New("memory.vector.enabled is not true in the config, so there are no vectors to reset")

// newMigrateCmd builds `sidekick migrate`, with `up`, `status` and
// `vector-reset`. They read the database URL from the secret that
// database.urlSecret in the config file names. regs are the plugin
// registries the config is checked against; the zero value means the
// global ones.
func newMigrateCmd(regs config.Registries) *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply or inspect the database migrations",
		Args:  cobra.NoArgs,
	}

	addConfigFlag(cmd, &configPath)

	cmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply every migration the database does not have yet",
			Long: `Apply every migration the database does not have yet. With
memory.vector.enabled, this includes the vector memory migrations, which
create the pgvector extension, and the column and index for the length of
vectors that memory.vector.embeddings.dimensions gives.`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, pool, err := openDatabase(cmd.Context(), regs, configPath)
				if err != nil {
					return fmt.Errorf("migrate up: %w", err)
				}
				defer pool.Close()

				if err := store.Migrate(cmd.Context(), pool); err != nil {
					return fmt.Errorf("migrate up: %w", err)
				}

				if cfg.Memory.Vector.Enabled {
					if err := store.MigrateVector(cmd.Context(), pool, cfg.Memory.Vector.Dimensions); err != nil {
						return fmt.Errorf("migrate up: %w", err)
					}
				}

				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "The database has every migration."); err != nil {
					return fmt.Errorf("write result: %w", err)
				}

				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "List each migration and whether the database has it",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, pool, err := openDatabase(cmd.Context(), regs, configPath)
				if err != nil {
					return fmt.Errorf("migrate status: %w", err)
				}
				defer pool.Close()

				return printMigrationStatus(cmd, pool, cfg.Memory.Vector.Enabled)
			},
		},
		newVectorResetCmd(regs, &configPath),
	)

	return cmd
}

// newVectorResetCmd builds `sidekick migrate vector-reset`.
func newVectorResetCmd(regs config.Registries, configPath *string) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "vector-reset",
		Short: "Empty the memory embeddings so they are made again, for a new vector length",
		Long: `Empty memory_embeddings and take the length off its embedding column. Run it
after changing memory.vector.embeddings.dimensions, which Sidekick refuses
to start with while the table holds vectors of the old length. The next
start gives the column the new length and embeds every memory entry again,
which costs one embeddings call for each entry. Embeddings are derived
data, so nothing else is lost. It writes one audit entry, with the old and
the new length. It asks for confirmation unless --yes is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, pool, err := openDatabase(cmd.Context(), regs, *configPath)
			if err != nil {
				return fmt.Errorf("migrate vector-reset: %w", err)
			}
			defer pool.Close()

			if !cfg.Memory.Vector.Enabled {
				return fmt.Errorf("migrate vector-reset: %w", errVectorsOff)
			}

			if !yes {
				if err := confirmReset(cmd); err != nil {
					return fmt.Errorf("migrate vector-reset: %w", err)
				}
			}

			return resetVectors(cmd, pool, cfg.Memory.Vector.Dimensions)
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")

	return cmd
}

// confirmReset asks whether to empty the embeddings and returns
// errResetDeclined unless the answer is yes.
func confirmReset(cmd *cobra.Command) error {
	if _, err := fmt.Fprint(cmd.OutOrStdout(),
		"This empties memory_embeddings. Sidekick embeds every memory entry again at its next start.\nType yes to continue: "); err != nil {
		return fmt.Errorf("write the question: %w", err)
	}

	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && answer == "" {
		return fmt.Errorf("read the answer: %w", errors.Join(err, errResetDeclined))
	}

	if strings.TrimSpace(strings.ToLower(answer)) != "yes" {
		return errResetDeclined
	}

	return nil
}

// resetVectors empties the embeddings, writes the audit entry in the same
// transaction and tells what to do next.
func resetVectors(cmd *cobra.Command, pool *pgxpool.Pool, dimensions int) error {
	type lengths struct {
		Dimensions int `json:"dimensions"`
	}

	writer := audit.NewWriter()

	old, err := store.ResetVectors(cmd.Context(), pool, func(ctx context.Context, tx pgx.Tx, oldDimensions int) error {
		if err := writer.Write(ctx, tx, audit.Entry{
			ActorKind:  audit.ActorSystem,
			Action:     auditVectorsReset,
			TargetKind: auditVectorsTable,
			Old:        lengths{Dimensions: oldDimensions},
			New:        lengths{Dimensions: dimensions},
		}); err != nil {
			return fmt.Errorf("write the audit entry: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("migrate vector-reset: %w", err)
	}

	if _, err := fmt.Fprintf(cmd.OutOrStdout(),
		"Emptied memory_embeddings, which held vectors of %d dimensions. Sidekick sets the column to %d dimensions and embeds every memory entry again at its next start.\n",
		old, dimensions); err != nil {
		return fmt.Errorf("write result: %w", err)
	}

	return nil
}

// openDatabase connects to the database that the config file at path
// names, and returns the config too. It reads no other secret, since a
// migration Job may not have the others mounted.
func openDatabase(ctx context.Context, regs config.Registries, path string) (*config.Config, *pgxpool.Pool, error) {
	cfg, err := config.Load(ctx, path, config.LoadOptions{Registries: regs})
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}

	url, err := cfg.DatabaseURL(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("find the database URL: %w", err)
	}

	pool, err := store.Open(ctx, url, migrateMaxConns)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to the database: %w", err)
	}

	return cfg, pool, nil
}

// printMigrationStatus lists the migrations of the main set, and with
// vector memory on, those of the vector set under their own heading.
func printMigrationStatus(cmd *cobra.Command, pool *pgxpool.Pool, vector bool) error {
	statuses, err := store.MigrationStatus(cmd.Context(), pool)
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}

	if err := printStatuses(cmd, statuses); err != nil {
		return fmt.Errorf("print the migrations: %w", err)
	}

	if !vector {
		return nil
	}

	statuses, err = store.VectorMigrationStatus(cmd.Context(), pool)
	if err != nil {
		return fmt.Errorf("list vector migrations: %w", err)
	}

	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "\nVector memory migrations (memory.vector.enabled):"); err != nil {
		return fmt.Errorf("write status: %w", err)
	}

	if err := printStatuses(cmd, statuses); err != nil {
		return fmt.Errorf("print the vector migrations: %w", err)
	}

	return nil
}

func printStatuses(cmd *cobra.Command, statuses []*goose.MigrationStatus) error {
	out := tabwriter.NewWriter(cmd.OutOrStdout(), tabMinWidth, tabWidth, tabPadding, tabPadChar, tabFlags)
	if _, err := fmt.Fprintln(out, "VERSION\tSTATE\tAPPLIED AT\tFILE"); err != nil {
		return fmt.Errorf("write status: %w", err)
	}

	for _, status := range statuses {
		appliedAt := "-"
		if !status.AppliedAt.IsZero() {
			appliedAt = status.AppliedAt.UTC().Format(time.DateTime) + " UTC"
		}

		if _, err := fmt.Fprintf(out, "%d\t%s\t%s\t%s\n", status.Source.Version, status.State, appliedAt, status.Source.Path); err != nil {
			return fmt.Errorf("write status: %w", err)
		}
	}

	if err := out.Flush(); err != nil {
		return fmt.Errorf("write status: %w", err)
	}

	return nil
}
