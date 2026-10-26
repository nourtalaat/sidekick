//go:build integration

package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

func TestMigrateCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// runs are the commands to run in order, after `sidekick`.
		runs [][]string
		// want and notWant are text the output of the last run must and
		// must not contain.
		want    []string
		notWant []string
	}{
		{
			name:    "status of a new database",
			runs:    [][]string{{"migrate", "status"}},
			want:    []string{"VERSION", "pending", "00001_baseline.sql"},
			notWant: []string{"applied"},
		},
		{
			name: "up applies the migrations",
			runs: [][]string{{"migrate", "up"}},
			want: []string{"The database has every migration."},
		},
		{
			name:    "status after up",
			runs:    [][]string{{"migrate", "up"}, {"migrate", "status"}},
			want:    []string{"applied", "00001_baseline.sql"},
			notWant: []string{"pending"},
		},
		{
			name: "up again finds nothing to do",
			runs: [][]string{{"migrate", "up"}, {"migrate", "up"}},
			want: []string{"The database has every migration."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			configPath := writeConfig(t, validConfig, storetest.NewEmptyURL(t))

			var out bytes.Buffer

			for _, args := range tt.runs {
				out.Reset()

				root := newTestRootCmd(newMigrateCmd(fakeRegistries()))
				root.SetOut(&out)
				root.SetErr(&bytes.Buffer{})
				root.SetArgs(append(args, "--config", configPath))
				require.NoError(t, root.Execute(), "sidekick %v", args)
			}

			for _, text := range tt.want {
				require.Contains(t, out.String(), text)
			}

			for _, text := range tt.notWant {
				require.NotContains(t, out.String(), text)
			}
		})
	}
}
