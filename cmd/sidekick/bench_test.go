package main

import (
	"bytes"
	"cmp"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// benchScenario is a scenario that loads, with STRATEGY to replace. Its one
// source is unavailable, so it needs no snapshot.
const benchScenario = `
id: disk-full
strategy: STRATEGY
now: 2026-10-26T09:10:00Z
event:
  adapter: alertmanager
  payload:
    version: "4"
    groupKey: g
    alerts:
      - { status: firing, labels: { alertname: DiskFull }, startsAt: "2026-10-26T09:00:00Z", fingerprint: f1 }
sources:
  k8s: { unavailable: true }
expected:
  verdict: true_positive
`

func TestBenchRunBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   string
		scenario string
		// args follow "bench run"; SCENARIOS and CONFIG are replaced.
		args       []string
		wantStderr string
	}{
		{
			name:       "repeat below 1",
			args:       []string{"--scenarios", "SCENARIOS", "--config", "CONFIG", "--repeat", "0"},
			wantStderr: "--repeat must be at least 1, not 0",
		},
		{
			name:       "no such scenario path",
			args:       []string{"--scenarios", "SCENARIOS/missing", "--config", "CONFIG"},
			wantStderr: "invalid scenario",
		},
		{
			name:       "a scenario with an unknown key",
			scenario:   benchScenario + "colour: red\n",
			args:       []string{"--scenarios", "SCENARIOS", "--config", "CONFIG"},
			wantStderr: "colour: unknown key",
		},
		{
			name:       "a scenario whose strategy is not loaded",
			scenario:   strings.Replace(benchScenario, "STRATEGY", "nope", 1),
			args:       []string{"--scenarios", "SCENARIOS", "--config", "CONFIG"},
			wantStderr: `strategy: "nope" is not loaded`,
		},
		{
			name:       "a config with a problem",
			config:     strings.Replace(validConfig, "investigate: claude", "investigate: claud", 1),
			args:       []string{"--scenarios", "SCENARIOS", "--config", "CONFIG"},
			wantStderr: `model.roles.investigate: connector "claud" is not defined`,
		},
		{
			name:       "--db without a readable database secret",
			args:       []string{"--scenarios", "SCENARIOS", "--config", "CONFIG", "--db"},
			wantStderr: "--db uses the database of database.urlSecret, which cannot be read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scenario := cmp.Or(tt.scenario, strings.Replace(benchScenario, "STRATEGY", "generic-alert", 1))
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "disk-full.yaml"), []byte(scenario), 0o600))

			configPath := writeConfig(t, cmp.Or(tt.config, validConfig), "")

			args := []string{"bench", "run"}
			for _, a := range tt.args {
				args = append(args, strings.NewReplacer("SCENARIOS", dir, "CONFIG", configPath).Replace(a))
			}

			var stdout, stderr bytes.Buffer

			root := newTestRootCmd(newBenchCmd(fakeRegistries()))
			root.SetArgs(args)
			root.SetOut(&stdout)
			root.SetErr(&stderr)

			err := root.ExecuteContext(t.Context())
			require.Error(t, err)
			require.Equal(t, exitBadInput, exitCode(err), "the error: %v", err)
			require.Contains(t, stderr.String(), tt.wantStderr)
			require.Empty(t, stdout.String(), "nothing ran")
		})
	}
}

func TestBenchJudgeCheckBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// args follow "bench judge-check"; CONFIG is replaced.
		args       []string
		wantStderr string
	}{
		{
			name:       "an agreement above 1",
			args:       []string{"--config", "CONFIG", "--min-agreement", "2"},
			wantStderr: "--min-agreement must be from 0 to 1, not 2",
		},
		{
			name:       "no such case path",
			args:       []string{"--config", "CONFIG", "--cases", "CASES/missing"},
			wantStderr: "invalid scenario",
		},
		{
			name:       "a case with an unknown field",
			args:       []string{"--config", "CONFIG", "--cases", "CASES"},
			wantStderr: `unknown field "colour"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "case.json"), []byte(`{"colour": 1}`), 0o600))

			configPath := writeConfig(t, validConfig, "")

			args := []string{"bench", "judge-check"}
			for _, a := range tt.args {
				args = append(args, strings.NewReplacer("CASES", dir, "CONFIG", configPath).Replace(a))
			}

			var stdout, stderr bytes.Buffer

			root := newTestRootCmd(newBenchCmd(fakeRegistries()))
			root.SetArgs(args)
			root.SetOut(&stdout)
			root.SetErr(&stderr)

			err := root.ExecuteContext(t.Context())
			require.Error(t, err)
			require.Equal(t, exitBadInput, exitCode(err), "the error: %v", err)
			require.Contains(t, err.Error()+stderr.String(), tt.wantStderr)
			require.Empty(t, stdout.String(), "nothing ran")
		})
	}
}
