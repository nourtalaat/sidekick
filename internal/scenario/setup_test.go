//go:build integration

package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/scrub"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/summarize"
)

// The rows the seed makes.
var (
	seedGroup    = uuid.MustParse("0198f2a4-0000-7000-8000-0000000000b1")
	seedEvent    = uuid.MustParse("0198f2a4-0000-7000-8000-0000000000a1")
	reviewerID   = uuid.MustParse("0198f2a4-0000-7000-8000-0000000000e1")
	spendDayTime = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
)

// rubricAnswer is what the fake summarize model answers.
const rubricAnswer = `{"items":[` +
	`{"text":"Names DB connection pool exhaustion on checkout-7f9c.prod.acme.com as the cause","weight":2,"required":true},` +
	`{"text":"Links the cause to commit abc1234def5678","weight":2,"required":true}]}`

// rubricUsage is the tokens of the fake summarize model's answer; with the
// prices below it costs rubricCostMicros.
var rubricUsage = model.Usage{InputTokens: 2000, OutputTokens: 100}

const rubricCostMicros = 2500

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// fixture is a database with the seed in it and a service over it.
type fixture struct {
	pool    *pgxpool.Pool
	service *Service
	seed    seed
	// connector is the fake summarize model, and nil when the service has
	// no rubric drafter.
	connector *modeltest.Connector
}

// newFixture seeds a database and builds a service. With rubric set, the
// service drafts rubrics with a fake summarize model that answers once.
func newFixture(t *testing.T, rubric bool) fixture {
	t.Helper()

	pool := storetest.New(t)
	s := loadSeed(t)

	seedDatabase(t, pool, s)

	cleaner, err := scrub.New(scrub.Options{HashKey: testKey})
	require.NoError(t, err)

	f := fixture{pool: pool, seed: s}

	var drafter RubricDrafter

	if rubric {
		f.connector = modeltest.New(t, model.Capabilities{ToolCalling: true, ToolChoice: true},
			modeltest.ToolUse(rubricUsage, modeltest.Call("c1", rubricToolName, rubricAnswer)))

		drafter = NewRubricDrafter(summarize.New(summarize.Options{
			Name: "summarizer", Connector: f.connector, Model: "summarizer-1",
			Prices: pricing.New(map[string]model.Price{"summarizer-1": {InputPerMTok: 1_000_000, OutputPerMTok: 5_000_000}}),
			Now:    func() time.Time { return spendDayTime },
		}))
	}

	sources := map[string]config.Section{}
	for name, typ := range s.SourceTypes {
		sources[name] = config.Section{Type: typ}
	}

	f.service = NewService(Options{
		Pool: pool, Cleaner: cleaner, Rubric: drafter, Audit: audit.NewWriter(), Sources: sources,
		Webhooks: map[string]config.Webhook{s.Event.Intake: {Section: config.Section{Type: s.Event.Adapter}}},
		Now:      func() time.Time { return spendDayTime },
	})

	return f
}

// seedDatabase writes the seed's event, investigation and steps, and a
// reviewer.
func seedDatabase(t *testing.T, pool *pgxpool.Pool, s seed) {
	t.Helper()

	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()

		_, err := pool.Exec(ctx, sql, args...)
		require.NoError(t, err)
	}

	exec(`INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, created_at, last_login_at)
	      VALUES ($1, 'https://issuer.example', 'reviewer', 'Rhea Reviewer', 'admin', now(), now())`, reviewerID)

	exec(`INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
	      VALUES ($1, 'fp-checkout', $2, 'checkout-api', 'open', 1, $3, $3, $4)`, seedGroup, s.Route, s.Event.ReceivedAt, s.InvestigationID)

	exec(`INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, service, labels, fingerprint, status, route, group_id, raw, received_at)
	      VALUES ($1, $2, 'checkout-1', 'alert', true, 'CheckoutLatencyHigh', $3, 'checkout-api', '{}', 'fp-checkout', 'grouped', $4, $5, $6, $7)`,
		seedEvent, s.Event.Intake, s.Title, s.Route, seedGroup, []byte(s.Event.Payload), s.Event.ReceivedAt)

	exec(`INSERT INTO investigations (id, group_id, route, strategy, mode, status, run, next_seq, verdict, confidence, connectors, budget,
	                                 created_at, finished_at, alert_title, service, confirmed_verdict, confirmed_cause, confirmed_by, confirmed_at)
	      VALUES ($1, $2, $3, $4, 'read-only', 'final', 1, 100, $5, 'high', '{}', '{}', $6, $6, $7, 'checkout-api', $5, $8, $9, $6)`,
		s.InvestigationID, seedGroup, s.Route, s.Strategy, string(s.Verdict), s.Event.ReceivedAt, s.Title, s.Cause, reviewerID)

	for i, c := range s.Calls {
		callID := fmt.Sprintf("c%d", i+1)

		turn, err := json.Marshal([]map[string]any{{"type": "tool_call", "id": callID, "name": c.Name, "input": c.Input}})
		require.NoError(t, err)

		result, err := json.Marshal([]map[string]any{{"type": "tool_result", "call_id": callID, "content": prompt.WrapUntrusted(c.Source, c.Name, callID, c.Text)}})
		require.NoError(t, err)

		evidence, err := json.Marshal(withCallID(c.Evidence, callID))
		require.NoError(t, err)

		exec(`INSERT INTO steps (investigation_id, seq, run, kind, parts, status, started_at, duration_ms)
		      VALUES ($1, $2, 1, 'model_turn', $3, 'ok', $4, 10)`, s.InvestigationID, c.Seq-1, turn, s.Event.ReceivedAt)

		exec(`INSERT INTO steps (investigation_id, seq, run, kind, parts, source, tool, tool_call_id, status, evidence, started_at, duration_ms)
		      VALUES ($1, $2, 1, 'tool_result', $3, $4, $5, $6, $7, $8, $9, 10)`,
			s.InvestigationID, c.Seq, result, c.Source, c.Tool, callID, c.Status, evidence, s.Event.ReceivedAt)
	}
}

// auditCount counts the audit entries of an action on a scenario.
func auditCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool, action, id string) int {
	t.Helper()

	var n int

	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = $1 AND target_id = $2`, action, id).Scan(&n))

	return n
}

// withCallID gives each evidence link the ID of the call that produced it.
func withCallID(evidence []source.Evidence, callID string) []source.Evidence {
	out := make([]source.Evidence, len(evidence))
	for i, e := range evidence {
		e.ToolCallID = callID
		out[i] = e
	}

	return out
}

// dayOf returns the day of t as spend_daily keeps it.
func dayOf(t time.Time) pgtype.Date {
	y, m, d := t.Date()

	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}
