package bench

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// checkedScenario is a scenario for the checks of New, with ADAPTER,
// STRATEGY, ALERTS and SNAPSHOT to replace.
const checkedScenario = `
id: checked
strategy: STRATEGY
now: 2026-05-04T10:20:00Z
event:
  adapter: ADAPTER
  payload:
    version: "4"
    groupKey: g
    alerts: ALERTS
sources:
  github:
    tools: { snapshot: SNAPSHOT }
    fixtures:
      - { tool: list_commits, respond: { text: none } }
expected:
  verdict: true_positive
`

// firing is one firing Alertmanager alert.
const firing = `{ status: firing, labels: { alertname: A }, startsAt: "2026-05-04T10:12:00Z", fingerprint: f1 }`

func TestNewChecksScenarios(t *testing.T) {
	t.Parallel()

	defaults := map[string]string{
		"ADAPTER":  "alertmanager",
		"STRATEGY": "generic-alert",
		"ALERTS":   "[" + firing + "]",
		"SNAPSHOT": "bench/tools/github.json",
	}

	tests := []struct {
		name string
		// set replaces some of the defaults.
		set map[string]string
		// wantErr is a part of the error; empty means the scenario passes.
		wantErr string
	}{
		{name: "a scenario that passes"},
		{name: "an adapter that does not exist", set: map[string]string{"ADAPTER": "pagerduty"}, wantErr: `event.adapter: build: build intake "bench-check": type "pagerduty": unknown type`},
		{name: "a strategy that is not loaded", set: map[string]string{"STRATEGY": "nope"}, wantErr: `strategy: "nope" is not loaded`},
		{
			name:    "two events",
			set:     map[string]string{"ALERTS": "[" + firing + ", " + strings.Replace(firing, "f1", "f2", 1) + "]"},
			wantErr: "event.payload: the payload must give exactly one event; the alertmanager adapter gives 2",
		},
		{name: "no event", set: map[string]string{"ALERTS": "[]"}, wantErr: "event.payload:"},
		{
			name:    "a recovery",
			set:     map[string]string{"ALERTS": "[" + strings.Replace(firing, "firing", "resolved", 1) + "]"},
			wantErr: "event.payload: the event is a recovery, which opens no investigation",
		},
		{name: "a payload the adapter cannot parse", set: map[string]string{"ALERTS": "nothing"}, wantErr: "event.payload: the alertmanager adapter cannot parse it"},
		{name: "a snapshot that does not exist", set: map[string]string{"SNAPSHOT": "bench/tools/missing.json"}, wantErr: "sources.github: build:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content := checkedScenario
			for key, value := range defaults {
				if v, ok := tt.set[key]; ok {
					value = v
				}

				content = strings.ReplaceAll(content, key, value)
			}

			file := filepath.Join(t.TempDir(), "checked.yaml")
			require.NoError(t, os.WriteFile(file, []byte(content), 0o600))

			scenarios, err := LoadScenarios(file, LoadOptions{BaseDir: repoRoot})
			require.NoError(t, err)

			cfg, regs := loadTestConfig(t, modeltest.New(t, model.Capabilities{ToolCalling: true}))

			_, err = New(t.Context(), scenarios, Options{Config: cfg, Registries: regs})
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidScenario)
			require.ErrorContains(t, err, file+": "+tt.wantErr)
		})
	}
}

func TestWriteTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		results Results
		want    string
	}{
		{
			name: "one row per run",
			results: Results{Repeat: 2, Scenarios: []ScenarioResult{{
				ID:       "checkout-pool-size",
				Expected: Expected{Verdict: report.TruePositive},
				Runs: []RunResult{
					{
						Run: 1, Status: "final", Report: report.Report{Verdict: report.TruePositive, StopReason: report.StopSubmitted},
						ToolCalls: 4, InputTokens: 3000, OutputTokens: 600, CostMicros: 21500, Duration: 1234567 * time.Microsecond,
					},
					{
						Run: 2, Status: "final", Report: report.Report{Verdict: report.Inconclusive, StopReason: report.StopBudget},
						ToolCalls: 30, InputTokens: 9000, OutputTokens: 900, CostMicros: 500000, Duration: 2 * time.Second,
					},
				},
			}}},
			want: "SCENARIO            RUN  STATUS  VERDICT        EXPECTED       STOP       TOOL CALLS  TOKENS  COST     TIME\n" +
				"checkout-pool-size  1/2  final   true_positive  true_positive  submitted  4           3600    $0.0215  1.235s\n" +
				"checkout-pool-size  2/2  final   inconclusive   true_positive  budget     30          9900    $0.5000  2s\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			require.NoError(t, tt.results.WriteTable(&out))
			require.Equal(t, tt.want, out.String())
		})
	}
}

func TestConfigDecoder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		target  any
		wantErr bool
	}{
		{name: "the config's own type", target: new(Limits)},
		{name: "another type", target: new(string), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			want := Limits{MaxToolCalls: 3}

			err := configDecoder[Limits]{cfg: want}.Decode(tt.target)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, &want, tt.target)
		})
	}
}
