package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// unreachableDB is a database URL nothing listens on, so connecting fails
// at once.
const unreachableDB = "postgres://sidekick:not-a-real-password@127.0.0.1:1/sidekick?sslmode=disable&connect_timeout=2" //nolint:gosec // A fake password nothing checks.

func TestDoctorCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     string
		dbURL      string
		wantErr    string
		wantOut    []string
		wantStderr string
	}{
		{
			name:    "every check runs and the failure sets the exit status",
			config:  validConfig,
			dbURL:   unreachableDB,
			wantErr: errDoctorFailed.Error(),
			wantOut: []string{
				"FAIL  database: cannot connect: ",
				"      fix: check the URL in the Secret sidekick-db, key url, and that Postgres 15 or newer accepts connections from Sidekick's pod",
				"skip  slack: no slack section",
				"skip  model claude: the anthropic provider has no API key check",
				"ok    prices: every configured model has a price",
				"skip  sources: no sources",
			},
		},
		{
			name:    "a missing database secret",
			config:  validConfig,
			wantErr: errDoctorFailed.Error(),
			wantOut: []string{"FAIL  database: database.urlSecret: cannot read secret sidekick-db/url: the file does not exist"},
		},
		{
			name:       "config problems are printed like config validate prints them",
			config:     validConfig + "servr: {}\n",
			wantErr:    errInvalidConfig.Error(),
			wantStderr: "servr: unknown key (did you mean \"server\"?)\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			root := newTestRootCmd(newDoctorCmd(fakeRegistries()))
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs([]string{"doctor", "--config", writeConfig(t, tt.config, tt.dbURL)})

			require.EqualError(t, root.Execute(), tt.wantErr)

			lines := strings.Split(stdout.String(), "\n")
			for _, want := range tt.wantOut {
				require.True(t, containsPrefix(lines, want), "no line starts with %q in:\n%s", want, stdout.String())
			}

			require.NotContains(t, stdout.String(), "not-a-real-password")
			require.Equal(t, tt.wantStderr, stderr.String())
		})
	}
}

// containsPrefix reports whether a line starts with prefix.
func containsPrefix(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}

	return false
}
