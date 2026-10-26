package main

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/store"
)

const (
	databaseURLFlag = "database-url"
	databaseURLEnv  = "SIDEKICK_DATABASE_URL"

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

var errNoDatabaseURL = errors.New("pass --" + databaseURLFlag + " or set " + databaseURLEnv)

// newMigrateCmd builds `sidekick migrate`, with `up` and `status`. getenv
// reads the environment; tests pass their own.
func newMigrateCmd(getenv func(string) string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply or inspect the database migrations",
		Args:  cobra.NoArgs,
	}

	// TODO: [SK-3] Read the database URL from config
	cmd.PersistentFlags().String(databaseURLFlag, "", "Postgres URL (default from $"+databaseURLEnv+")")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply every migration the database does not have yet",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				pool, err := openDatabase(cmd, getenv)
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
				pool, err := openDatabase(cmd, getenv)
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

// openDatabase connects to the database that the flag or the environment
// names.
func openDatabase(cmd *cobra.Command, getenv func(string) string) (*pgxpool.Pool, error) {
	url, err := databaseURL(cmd, getenv)
	if err != nil {
		return nil, fmt.Errorf("find the database URL: %w", err)
	}

	pool, err := store.Open(cmd.Context(), url, migrateMaxConns)
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

func databaseURL(cmd *cobra.Command, getenv func(string) string) (string, error) {
	url, err := cmd.Flags().GetString(databaseURLFlag)
	if err != nil {
		return "", fmt.Errorf("read --%s: %w", databaseURLFlag, err)
	}

	if url == "" {
		url = getenv(databaseURLEnv)
	}

	if url == "" {
		return "", errNoDatabaseURL
	}

	return url, nil
}
