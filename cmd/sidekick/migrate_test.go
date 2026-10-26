package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrateCommandErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		// dbURL is the database URL in the config's secret; empty leaves the
		// secret file out.
		dbURL   string
		wantErr string
	}{
		{
			name:    "default config path",
			args:    []string{"migrate", "up"},
			wantErr: "migrate up: load config: read config: open " + defaultConfigPath + ": no such file or directory",
		},
		{
			name:    "status without the database URL secret",
			args:    []string{"migrate", "status", "-c", "CONFIG"},
			wantErr: "migrate status: find the database URL: read database.urlSecret: resolve text secret: read secret sidekick-db/url",
		},
		{
			name:    "database URL that cannot be parsed",
			args:    []string{"migrate", "up", "--config", "CONFIG"},
			dbURL:   "postgres://localhost:notaport/sidekick",
			wantErr: "parse database URL",
		},
		{
			name:    "config with problems",
			args:    []string{"migrate", "up", "-c", "INVALID"},
			wantErr: `servr: unknown key (did you mean "server"?)`,
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

			paths := map[string]string{
				"CONFIG":  writeConfig(t, validConfig, tt.dbURL),
				"INVALID": writeConfig(t, validConfig+"servr: {}\n", tt.dbURL),
			}

			args := make([]string, len(tt.args))
			for i, a := range tt.args {
				args[i] = a
				if p, ok := paths[a]; ok {
					args[i] = p
				}
			}

			root := newTestRootCmd(newMigrateCmd(fakeRegistries()))
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(args)

			require.ErrorContains(t, root.Execute(), tt.wantErr)
		})
	}
}
