package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
)

func TestServeCommandErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     string
		dbURL      string
		args       []string
		wantErr    string
		wantStderr string
	}{
		{
			name:    "default config path",
			args:    []string{"serve"},
			wantErr: "start: load config: read config: open " + defaultConfigPath + ": no such file or directory",
		},
		{
			name:       "config problems are printed one per line",
			config:     strings.Replace(validConfig, "investigate: claude", "investigate: claud", 1) + "servr: {}\n",
			dbURL:      "postgres://localhost/sidekick",
			args:       []string{"serve", "-c", "CONFIG"},
			wantErr:    errInvalidConfig.Error(),
			wantStderr: "model.roles.investigate: connector \"claud\" is not defined (did you mean \"claude\"?)\nservr: unknown key (did you mean \"server\"?)\n",
		},
		{
			name:       "start-up checks every secret",
			config:     validConfig,
			args:       []string{"serve", "--config", "CONFIG"},
			wantErr:    errInvalidConfig.Error(),
			wantStderr: "database.urlSecret: cannot read secret sidekick-db/url: the file does not exist\n",
		},
		{
			name:    "extra arguments",
			args:    []string{"serve", "extra"},
			wantErr: `unknown command "extra" for "sidekick serve"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := make([]string, len(tt.args))
			for i, a := range tt.args {
				args[i] = a
				if a == "CONFIG" {
					args[i] = writeConfig(t, tt.config, tt.dbURL)
				}
			}

			var stderr bytes.Buffer

			root := newTestRootCmd(newServeCmd(fakeRegistries(), buildinfo.Info{Version: "dev", Commit: "unknown"}))
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&stderr)
			root.SetArgs(args)

			require.EqualError(t, root.Execute(), tt.wantErr)

			if tt.wantStderr != "" {
				require.Equal(t, tt.wantStderr, stderr.String())
			}
		})
	}
}
