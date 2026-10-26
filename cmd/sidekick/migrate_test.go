package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestDatabaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    string
		wantErr error
	}{
		{
			name:    "neither flag nor environment",
			wantErr: errNoDatabaseURL,
		},
		{
			name: "flag",
			args: []string{"--database-url", "postgres://flag/sidekick"},
			want: "postgres://flag/sidekick",
		},
		{
			name: "environment",
			env:  map[string]string{databaseURLEnv: "postgres://env/sidekick"},
			want: "postgres://env/sidekick",
		},
		{
			name: "flag wins over environment",
			args: []string{"--database-url", "postgres://flag/sidekick"},
			env:  map[string]string{databaseURLEnv: "postgres://env/sidekick"},
			want: "postgres://flag/sidekick",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			getenv := func(key string) string { return tt.env[key] }

			up, _, err := newMigrateCmd(getenv).Find([]string{"up"})
			require.NoError(t, err)
			require.NoError(t, up.ParseFlags(tt.args))

			got, err := databaseURL(up, getenv)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMigrateCommandErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "up without a database URL",
			args:    []string{"migrate", "up"},
			wantErr: "migrate up: find the database URL: pass --database-url or set SIDEKICK_DATABASE_URL",
		},
		{
			name:    "status without a database URL",
			args:    []string{"migrate", "status"},
			wantErr: "migrate status: find the database URL: pass --database-url or set SIDEKICK_DATABASE_URL",
		},
		{
			name:    "database URL that cannot be parsed",
			args:    []string{"migrate", "up", "--database-url", "postgres://localhost:notaport/sidekick"},
			wantErr: "parse database URL",
		},
		{
			name:    "extra arguments",
			args:    []string{"migrate", "up", "extra"},
			wantErr: `unknown command "extra" for "sidekick migrate up"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newTestRootCmd(func(string) string { return "" })
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(tt.args)

			require.ErrorContains(t, root.Execute(), tt.wantErr)
		})
	}
}

// newTestRootCmd builds a root command holding only `migrate`, which reads
// the environment through getenv instead of os.Getenv.
func newTestRootCmd(getenv func(string) string) *cobra.Command {
	root := &cobra.Command{Use: "sidekick", SilenceUsage: true}
	root.AddCommand(newMigrateCmd(getenv))

	return root
}
