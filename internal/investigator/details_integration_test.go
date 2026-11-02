//go:build integration

package investigator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// withDetails is the route block of a prompt for the payments route with
// the details of paymentsDetails, sorted by key.
const withDetails = "Route: payments\nenvironment: prod\nowner: payments team\n\n## First event"

// withoutDetails is the route block of a prompt for a route without details.
const withoutDetails = "Route: payments\n\n## First event"

var paymentsDetails = map[string]string{"owner": "payments team", "environment": "prod"}

func TestRouteDetailsReachThePrompts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		details map[string]string
		triage  bool
		want    string
	}{
		{name: "the investigation prompt", details: paymentsDetails, want: withDetails},
		{name: "a route without details", want: withoutDetails},
		{name: "the triage prompt and the investigation prompt", details: paymentsDetails, triage: true, want: withDetails},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := options{
				route: Route{Details: tt.details},
				turns: []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, notSure))},
			}

			if tt.triage {
				opts.triageTurns = []modeltest.Turn{triageTurn(triageLatency)}
			}

			h := setup(t, opts)

			require.NoError(t, h.run(t))
			require.Contains(t, userText(h.conn.Requests()[0].Messages[0]), tt.want)

			if tt.triage {
				require.Contains(t, userText(h.triage.Requests()[0].Messages[0]), tt.want)
			}
		})
	}
}

// TestRuntimeRouteDetailsReachThePrompt saves a runtime route with details
// through one replica and checks that a run on another replica shows them.
func TestRuntimeRouteDetailsReachThePrompt(t *testing.T) {
	t.Parallel()

	h := setup(t, options{turns: []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, notSure))}})
	storeA, cacheB := replicas(t, h)

	saveDetails(t, storeA, paymentsDetails)

	require.Eventually(t, func() bool {
		r, ok := cacheRoute(cacheB, "payments")

		return ok && len(r.Details) == len(paymentsDetails)
	}, replicaDeadline, 10*time.Millisecond)

	require.NoError(t, h.run(t))
	require.Contains(t, userText(h.conn.Requests()[0].Messages[0]), withDetails)
}

// saveDetails saves the runtime route payments through store with details.
func saveDetails(t *testing.T, store *rules.Store, details map[string]string) {
	t.Helper()

	_, err := store.SetRoute(t.Context(), routeActor, "payments", func(r *rules.Route) error {
		r.Config.Match = &config.Match{Service: "checkout-*"}
		r.Config.Outputs = []string{"slack-thread"}
		r.Config.Details = details

		return nil
	})
	require.NoError(t, err)
}
