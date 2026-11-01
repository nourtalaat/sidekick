//go:build integration

package investigator

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/strategystore"
	"github.com/nourtalaat/sidekick/strategies"
)

// TestInvestigationKeepsItsStrategyVersion saves a new version of the
// investigation's strategy before its first run and again before a
// follow-up, and checks that both runs use the version it started with.
func TestInvestigationKeepsItsStrategyVersion(t *testing.T) {
	t.Parallel()

	h := setup(t, options{turns: append(firstRun(), turn(modeltest.Call("a1", report.AnswerFollowUpTool, answerC1)))})

	base, err := strategy.Load(strategies.FS, "", nil)
	require.NoError(t, err)

	store := strategystore.NewStore(h.pool)
	cache := strategystore.NewCache(strategystore.Options{Base: base, Pool: h.pool})
	h.handler.opts.Strategies = cache
	h.handler.triager.strategies = cache

	save := func(marker string) {
		t.Helper()

		_, err := store.Save(t.Context(), strategystore.SaveInput{
			Actor:  strategystore.Actor{Kind: audit.ActorUser, ID: uuid.NewString()},
			Name:   strategy.GenericAlert,
			Body:   "name: generic-alert\ndescription: " + marker + "\n",
			Reason: "Test " + marker,
		})
		require.NoError(t, err)
		require.NoError(t, cache.Reload(t.Context()))
	}

	save("Marker one.")

	_, err = h.pool.Exec(t.Context(), "UPDATE investigations SET strategy = 'generic-alert', strategy_version = 'runtime:1' WHERE id = $1", testInvestigation)
	require.NoError(t, err)

	save("Marker two.")

	require.NoError(t, h.run(t))

	reqs := h.conn.Requests()
	require.Contains(t, reqs[0].System[1].Text, "Marker one.", "the first run uses the version the investigation started with")
	require.NotContains(t, reqs[0].System[1].Text, "Marker two.")
	require.Equal(t, "runtime:1", deref(h.investigation(t).StrategyVersion))

	save("Marker three.")

	require.True(t, h.ask(t, "Ev1", askPods))
	require.NoError(t, h.runFollowUp(t))

	reqs = h.conn.Requests()
	require.Len(t, reqs, 3)

	var strategyTexts []string
	for _, part := range reqs[2].System {
		strategyTexts = append(strategyTexts, part.Text)
	}

	joined := strings.Join(strategyTexts, "\n")
	require.Contains(t, joined, "Marker one.", "the follow-up resumes with the strategy text of the first run")
	require.NotContains(t, joined, "Marker three.")
	require.Equal(t, "runtime:1", deref(h.investigation(t).StrategyVersion))

	current, _ := cache.Current().Get(strategy.GenericAlert)
	require.Equal(t, "runtime:3", current.Version, "new investigations get the newest version")
}
