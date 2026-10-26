package main

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

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

// newMigrateCmd builds `sidekick migrate`, with `up` and `status`. They
// read the database URL from the secret that database.urlSecret in the
// config file names. regs are the plugin registries the config is checked
// against; the zero value means the global ones.
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
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				pool, err := openDatabase(cmd.Context(), regs, configPath)
				if err != nil {
					return fmt.Errorf("migrate up: %w", err)
				}
				defer pool.Close()

				if err := store.Migrate(cmd.Context(), pool); err != nil {
					return fmt.Errorf("migrate up: %w", err)
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
				pool, err := openDatabase(cmd.Context(), regs, configPath)
				if err != nil {
					return fmt.Errorf("migrate status: %w", err)
				}
				defer pool.Close()

				return printMigrationStatus(cmd, pool)
			},
		},
	)

	return cmd
}

// openDatabase connects to the database that the config file at path
// names. It reads no other secret, since a migration Job may not have the
// others mounted.
func openDatabase(ctx context.Context, regs config.Registries, path string) (*pgxpool.Pool, error) {
	cfg, err := config.Load(ctx, path, config.LoadOptions{Registries: regs})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	url, err := cfg.DatabaseURL(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("find the database URL: %w", err)
	}

	pool, err := store.Open(ctx, url, migrateMaxConns)
	if err != nil {
		return nil, fmt.Errorf("connect to the database: %w", err)
	}

	return pool, nil
}

func printMigrationStatus(cmd *cobra.Command, pool *pgxpool.Pool) error {
	statuses, err := store.MigrationStatus(cmd.Context(), pool)
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}

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
