package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/strategies"
)

func TestBuildTriageGolden(t *testing.T) {
	t.Parallel()

	set, err := strategy.Load(strategies.FS, "", nil)
	require.NoError(t, err)

	var all []strategy.Strategy

	for _, name := range set.Names() {
		st, _ := set.Get(name)
		all = append(all, st)
	}

	tool := model.ToolSpec{Name: SubmitTriageTool, Description: "Submit the triage decision.", InputSchema: json.RawMessage(`{"type":"object"}`)}
	startsAt := time.Date(2026, 10, 26, 8, 58, 0, 0, time.UTC)
	receivedAt := time.Date(2026, 10, 26, 9, 0, 5, 0, time.UTC)

	tests := []struct {
		name string
		in   TriageInput
	}{
		{
			name: "triage-latency-alert",
			in: TriageInput{
				Strategies:    all,
				RouteStrategy: strategy.GenericAlert,
				Tool:          tool,
				Group: Group{
					Count: 4,
					First: event.Event{
						Intake:     "alertmanager",
						Kind:       event.KindAlert,
						AlertName:  "CheckoutLatencyP99",
						Title:      "checkout-api p99 latency above 2s",
						Service:    "checkout-api",
						Severity:   event.SeverityWarning,
						StartsAt:   startsAt,
						ReceivedAt: receivedAt,
					},
				},
				Route: RouteContext{Name: "payments"},
			},
		},
		{
			name: "triage-no-strategy-fits",
			in: TriageInput{
				Strategies:    all,
				RouteStrategy: strategy.GenericAlert,
				Tool:          tool,
				Group: Group{
					Count: 1,
					First: event.Event{
						Intake:     "slack-mention",
						Kind:       event.KindMessage,
						Text:       "Can someone look at the nightly billing export?",
						Sender:     event.Sender{Kind: event.SenderHuman, ID: "U123"},
						ReceivedAt: receivedAt,
					},
				},
				Route: RouteContext{Name: "default"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := BuildTriage(tt.in)
			require.NotContains(t, triageSystemPrompt, "<no value>")
			require.Len(t, req.Tools, 1)
			require.True(t, req.Tools[0].Cache, "cache breakpoint after the tool")
			require.False(t, tt.in.Tool.Cache, "BuildTriage must not change the caller's tool")

			got := dumpRequest(t, req)
			path := filepath.Join("fixture", tt.name+".golden")

			if *update {
				require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
			}

			want, err := os.ReadFile(path) //nolint:gosec // A fixture path chosen by the test.
			require.NoError(t, err, "run go test ./internal/prompt -update to create the golden file")
			require.Equal(t, string(want), got)
		})
	}
}
