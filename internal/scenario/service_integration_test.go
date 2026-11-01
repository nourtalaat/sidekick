//go:build integration

package scenario

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/scrub"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// admin is who the tests act as.
var admin = Actor{Kind: audit.ActorUser, ID: reviewerID.String(), UserID: &reviewerID}

// parseGolden reads the golden scenario.
func parseGolden(t *testing.T) bench.Scenario {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("fixture", "golden.yaml")) //nolint:gosec // A fixture file.
	require.NoError(t, err)

	sc, err := bench.ParseScenario(raw)
	require.NoError(t, err)

	return sc
}

func TestExport(t *testing.T) {
	t.Parallel()

	f := newFixture(t, true)

	row, created, err := f.service.Export(t.Context(), f.seed.InvestigationID, admin)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "draft", row.Status)
	require.EqualValues(t, 1, row.Revision)
	require.Equal(t, &f.seed.InvestigationID, row.FromInvestigation)
	require.Equal(t, &reviewerID, row.CreatedBy)
	require.Nil(t, row.ReviewedBy)

	// The draft the database export makes is the golden scenario. The
	// payload comes back from jsonb with its keys in jsonb's order, so the
	// scenarios are compared as values.
	got, err := bench.ParseScenario([]byte(row.Body))
	require.NoError(t, err)
	require.Equal(t, parseGolden(t), got)
	require.Equal(t, "latency-alert-checkout-api-p99-latency-above-2s-for-5m-3f6e8d1c4b70", row.ID)

	require.Equal(t, 1, auditCount(t.Context(), t, f.pool, ActionDrafted, row.ID))

	// The rubric call is in the day's spend of the route.
	spend, err := db.New(f.pool).GetDailySpend(t.Context(), db.GetDailySpendParams{Route: f.seed.Route, Day: dayOf(spendDayTime)})
	require.NoError(t, err)
	require.EqualValues(t, rubricCostMicros, spend.RouteMicros)

	// Asking again returns the same scenario and calls no model.
	again, created, err := f.service.Export(t.Context(), f.seed.InvestigationID, admin)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, row.ID, again.ID)
	require.Equal(t, 1, auditCount(t.Context(), t, f.pool, ActionDrafted, row.ID))
	f.connector.ExpectAllUsed()
}

func TestExportWithoutRubricDrafter(t *testing.T) {
	t.Parallel()

	f := newFixture(t, false)

	row, _, err := f.service.Export(t.Context(), f.seed.InvestigationID, admin)
	require.NoError(t, err)

	got, err := bench.ParseScenario([]byte(row.Body))
	require.NoError(t, err)
	require.Empty(t, got.Expected.RootCause.Rubric)
	require.NotEmpty(t, got.Expected.RootCause.Summary)

	detail, err := f.service.Detail(t.Context(), row.ID)
	require.NoError(t, err)
	require.Equal(t, NoteNoRubric, detail.Notes[len(detail.Notes)-1].Kind)
}

func TestExportErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// change breaks the seeded investigation.
		change func(ctx context.Context, t *testing.T, f fixture)
		id     func(f fixture) uuid.UUID
		want   error
	}{
		{
			name: "no such investigation",
			id:   func(fixture) uuid.UUID { return uuid.New() },
			want: ErrInvestigationNotFound,
		},
		{
			name: "not confirmed",
			change: func(ctx context.Context, t *testing.T, f fixture) {
				t.Helper()

				_, err := f.pool.Exec(ctx, `UPDATE investigations SET confirmed_verdict = NULL, confirmed_cause = NULL`)
				require.NoError(t, err)
			},
			want: ErrNotConfirmed,
		},
		{
			name: "the first alert has no original payload",
			change: func(ctx context.Context, t *testing.T, f fixture) {
				t.Helper()

				_, err := f.pool.Exec(ctx, `UPDATE events SET raw = NULL`)
				require.NoError(t, err)
			},
			want: ErrNoEvent,
		},
		{
			name: "the first event is not an alert",
			change: func(ctx context.Context, t *testing.T, f fixture) {
				t.Helper()

				_, err := f.pool.Exec(ctx, `UPDATE events SET kind = 'mention'`)
				require.NoError(t, err)
			},
			want: ErrNoEvent,
		},
		{
			name: "the endpoint is not in the config",
			change: func(ctx context.Context, t *testing.T, f fixture) {
				t.Helper()

				_, err := f.pool.Exec(ctx, `UPDATE events SET intake = 'removed-endpoint'`)
				require.NoError(t, err)
			},
			want: ErrUnknownAdapter,
		},
		{
			name: "no source was called",
			change: func(ctx context.Context, t *testing.T, f fixture) {
				t.Helper()

				_, err := f.pool.Exec(ctx, `DELETE FROM steps`)
				require.NoError(t, err)
			},
			want: ErrNoSourceCalls,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, false)

			if tt.change != nil {
				tt.change(t.Context(), t, f)
			}

			id := f.seed.InvestigationID
			if tt.id != nil {
				id = tt.id(f)
			}

			_, _, err := f.service.Export(t.Context(), id, admin)
			require.ErrorIs(t, err, tt.want)

			var n int

			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM scenarios`).Scan(&n))
			require.Zero(t, n)
		})
	}
}

func TestReviewFlow(t *testing.T) {
	t.Parallel()

	f := newFixture(t, true)
	ctx := t.Context()

	draft, _, err := f.service.Export(ctx, f.seed.InvestigationID, admin)
	require.NoError(t, err)

	// A draft can be neither downloaded nor loaded to run.
	_, err = f.service.Download(ctx, draft.ID, admin)
	require.ErrorIs(t, err, ErrNotReviewed)

	_, err = LoadOneReviewed(ctx, f.pool, draft.ID, "")
	require.ErrorIs(t, err, ErrNotReviewed)

	loaded, err := LoadReviewed(ctx, f.pool, "")
	require.NoError(t, err)
	require.Empty(t, loaded)

	_, err = f.service.Download(ctx, "no-such-scenario", admin)
	require.ErrorIs(t, err, ErrNotFound)

	// Review needs the tick, the revision the reviewer read and a scenario.
	_, err = f.service.Review(ctx, draft.ID, draft.Revision, false, reviewerID, admin)
	require.ErrorIs(t, err, ErrNotChecked)

	_, err = f.service.Review(ctx, draft.ID, draft.Revision+1, true, reviewerID, admin)
	require.ErrorIs(t, err, ErrStale)

	_, err = f.service.Review(ctx, "no-such-scenario", 1, true, reviewerID, admin)
	require.ErrorIs(t, err, ErrNotFound)

	reviewed, err := f.service.Review(ctx, draft.ID, draft.Revision, true, reviewerID, admin)
	require.NoError(t, err)
	require.Equal(t, "reviewed", reviewed.Status)
	require.Equal(t, &reviewerID, reviewed.ReviewedBy)
	require.NotNil(t, reviewed.ReviewedAt)
	require.Equal(t, 1, auditCount(ctx, t, f.pool, ActionReviewed, draft.ID))

	_, err = f.service.Review(ctx, draft.ID, draft.Revision, true, reviewerID, admin)
	require.ErrorIs(t, err, ErrAlreadyReviewed)

	// A reviewed scenario can be downloaded and loaded to run.
	body, err := f.service.Download(ctx, draft.ID, admin)
	require.NoError(t, err)
	require.Equal(t, draft.Body, string(body))
	require.Equal(t, 1, auditCount(ctx, t, f.pool, ActionDownloaded, draft.ID))

	loaded, err = LoadReviewed(ctx, f.pool, "/repo")
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, draft.ID, loaded[0].ID)
	require.Equal(t, "scenario:"+draft.ID, loaded[0].Path)
	require.Equal(t, "/repo/bench/tools/github.json", loaded[0].Sources["github"].Tools.Snapshot)

	// Editing puts it back to draft, and it can no longer be used.
	edited := strings.Replace(draft.Body, "title: checkout-api p99 latency above 2s for 5m", "title: Checkout latency after a pool change", 1)
	require.NotEqual(t, draft.Body, edited)

	updated, err := f.service.Update(ctx, draft.ID, reviewed.Revision, []byte(edited), admin)
	require.NoError(t, err)
	require.Equal(t, "draft", updated.Status)
	require.Nil(t, updated.ReviewedBy)
	require.Nil(t, updated.ReviewedAt)
	require.Equal(t, reviewed.Revision+1, updated.Revision)
	require.Equal(t, "Checkout latency after a pool change", updated.Title)
	require.Equal(t, 1, auditCount(ctx, t, f.pool, ActionUpdated, draft.ID))

	_, err = f.service.Download(ctx, draft.ID, admin)
	require.ErrorIs(t, err, ErrNotReviewed)

	loaded, err = LoadReviewed(ctx, f.pool, "")
	require.NoError(t, err)
	require.Empty(t, loaded)
}

func TestUpdateRefusals(t *testing.T) {
	t.Parallel()

	f := newFixture(t, false)
	ctx := t.Context()

	draft, _, err := f.service.Export(ctx, f.seed.InvestigationID, admin)
	require.NoError(t, err)

	tests := []struct {
		name     string
		id       string
		revision int32
		body     string
		want     error
		wantLeak bool
	}{
		{name: "stale revision", id: draft.ID, revision: draft.Revision + 1, body: draft.Body, want: ErrStale},
		{name: "no such scenario", id: "no-such-scenario", revision: 1, body: strings.Replace(draft.Body, draft.ID, "no-such-scenario", 1), want: ErrNotFound},
		{name: "not YAML", id: draft.ID, revision: draft.Revision, body: "id: [", want: ErrInvalid},
		{name: "not a scenario", id: draft.ID, revision: draft.Revision, body: "id: " + draft.ID + "\n", want: ErrInvalid},
		{name: "another id", id: draft.ID, revision: draft.Revision, body: strings.Replace(draft.Body, "id: "+draft.ID, "id: another-id", 1), want: ErrInvalid},
		{
			name: "an email", id: draft.ID, revision: draft.Revision, wantLeak: true,
			body: strings.Replace(draft.Body, "title: checkout-api p99 latency above 2s for 5m", "title: ask bob@acme.com", 1),
		},
		{
			name: "a host", id: draft.ID, revision: draft.Revision, wantLeak: true,
			body: strings.Replace(draft.Body, "title: checkout-api p99 latency above 2s for 5m", "title: db-9.prod.acme.com is down", 1),
		},
	}

	t.Cleanup(func() {
		// Nothing was written.
		row, err := db.New(f.pool).GetScenario(context.WithoutCancel(ctx), draft.ID)
		require.NoError(t, err)
		require.Equal(t, draft.Revision, row.Revision)
		require.Equal(t, draft.Body, row.Body)
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := f.service.Update(ctx, tt.id, tt.revision, []byte(tt.body), admin)
			if tt.wantLeak {
				var leak *scrub.LeakError

				require.ErrorAs(t, err, &leak)

				return
			}

			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestDetail(t *testing.T) {
	t.Parallel()

	f := newFixture(t, true)
	ctx := t.Context()

	draft, _, err := f.service.Export(ctx, f.seed.InvestigationID, admin)
	require.NoError(t, err)

	detail, err := f.service.Detail(ctx, draft.ID)
	require.NoError(t, err)

	pairs := map[string]Pair{}
	for _, p := range detail.Pairs {
		pairs[p.Ref] = p
	}

	// Each part is next to its original.
	logs := pairs["sources.signoz.fixtures[0].responses[0].text"]
	require.NotNil(t, logs.Original)
	require.Equal(t, f.seed.Calls[0].Text, *logs.Original)
	require.Contains(t, logs.Cleaned, "host=host-1")
	require.Empty(t, logs.Note)

	require.NotContains(t, logs.Cleaned, "checkout-7f9c")

	match := pairs["sources.github.fixtures[1].match"]
	require.NotNil(t, match.Original)
	require.Contains(t, *match.Original, `"since"`)
	require.NotContains(t, match.Cleaned, "since")

	event := pairs["event.payload"]
	require.NotNil(t, event.Original)
	require.Contains(t, *event.Original, "dana.reyes@acme.com")
	require.NotContains(t, event.Cleaned, "dana.reyes")

	cause := pairs["expected.rootCause.summary"]
	require.NotNil(t, cause.Original)
	require.Equal(t, f.seed.Cause, *cause.Original)

	// The trace ID and the owner of the repository were kept and are flagged.
	require.Len(t, detail.Candidates, 2)
	require.Contains(t, detail.Candidates[0].Path, "github")
	require.Contains(t, detail.Candidates[1].Path, "signoz")

	// Once retention deletes the steps, the originals are gone.
	_, err = f.pool.Exec(ctx, `DELETE FROM steps`)
	require.NoError(t, err)

	detail, err = f.service.Detail(ctx, draft.ID)
	require.NoError(t, err)

	for _, p := range detail.Pairs {
		if strings.HasPrefix(p.Ref, "sources.") {
			require.Nil(t, p.Original, p.Ref)
			require.Equal(t, noteGone, p.Note, p.Ref)
		}
	}

	_, err = f.service.Detail(ctx, "no-such-scenario")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestList(t *testing.T) {
	t.Parallel()

	f := newFixture(t, false)
	ctx := t.Context()
	q := db.New(f.pool)

	base := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

	for i, id := range []string{"first", "second", "third"} {
		_, err := q.InsertScenario(ctx, db.InsertScenarioParams{
			ID: id, Title: id, Body: "id: " + id, Provenance: []byte(`[]`), Notes: []byte(`[]`), Now: base.Add(time.Duration(i) * time.Hour),
		})
		require.NoError(t, err)
	}

	_, err := f.pool.Exec(ctx, `UPDATE scenarios SET status = 'reviewed', reviewed_by = $1, reviewed_at = now() WHERE id = 'second'`, reviewerID)
	require.NoError(t, err)

	ids := func(rows []Summary) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.ID
		}

		return out
	}

	page, next, err := f.service.List(ctx, "", nil, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"third", "second"}, ids(page))
	require.NotNil(t, next)

	page, next, err = f.service.List(ctx, "", next, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"first"}, ids(page))
	require.Nil(t, next)

	page, next, err = f.service.List(ctx, "reviewed", nil, 5)
	require.NoError(t, err)
	require.Equal(t, []string{"second"}, ids(page))
	require.Nil(t, next)

	page, _, err = f.service.List(ctx, "draft", nil, 5)
	require.NoError(t, err)
	require.Equal(t, []string{"third", "first"}, ids(page))
}

// copyInvestigation makes a second investigation of the seed's alert, with
// the same title, strategy and steps, under the ID newID.
func copyInvestigation(t *testing.T, f fixture, newID uuid.UUID) {
	t.Helper()

	_, err := f.pool.Exec(t.Context(), `INSERT INTO investigations
		(id, group_id, route, strategy, mode, status, run, next_seq, verdict, confidence, connectors, budget, created_at, finished_at,
		 alert_title, service, confirmed_verdict, confirmed_cause, confirmed_by, confirmed_at)
		SELECT $2, group_id, route, strategy, mode, status, run, next_seq, verdict, confidence, connectors, budget, created_at, finished_at,
		       alert_title, service, confirmed_verdict, confirmed_cause, confirmed_by, confirmed_at
		FROM investigations WHERE id = $1`, f.seed.InvestigationID, newID)
	require.NoError(t, err)

	_, err = f.pool.Exec(t.Context(), `INSERT INTO steps
		(investigation_id, seq, run, kind, parts, source, tool, tool_call_id, status, evidence, started_at, duration_ms)
		SELECT $2, seq, run, kind, parts, source, tool, tool_call_id, status, evidence, started_at, duration_ms
		FROM steps WHERE investigation_id = $1`, f.seed.InvestigationID, newID)
	require.NoError(t, err)
}

func TestExportOfTheSameAlertTwiceInOneWindow(t *testing.T) {
	t.Parallel()

	f := newFixture(t, false)

	// UUIDv7 IDs made within hours of each other share their first six hex
	// characters; the scenario IDs must still differ.
	second := uuid.MustParse("0198f2ff-1111-7000-8000-00000000abcd")
	require.Equal(t, f.seed.InvestigationID.String()[:6], second.String()[:6])

	copyInvestigation(t, f, second)

	first, created, err := f.service.Export(t.Context(), f.seed.InvestigationID, admin)
	require.NoError(t, err)
	require.True(t, created)

	other, created, err := f.service.Export(t.Context(), second, admin)
	require.NoError(t, err)
	require.True(t, created)

	require.NotEqual(t, first.ID, other.ID)
	require.Equal(t, first.Title, other.Title)
}

func TestExportKeepsTheSpendOfARubricThatWasNotSaved(t *testing.T) {
	t.Parallel()

	f := newFixture(t, true)

	// A scenario of another investigation has the ID the export will take, so
	// the insert fails after the rubric was drafted and paid for.
	_, err := f.pool.Exec(t.Context(), `INSERT INTO scenarios (id, title, status, body, provenance, notes, created_at, updated_at)
		VALUES ('latency-alert-checkout-api-p99-latency-above-2s-for-5m-3f6e8d1c4b70', 'taken', 'draft', 'id: x', '[]', '[]', now(), now())`)
	require.NoError(t, err)

	_, _, err = f.service.Export(t.Context(), f.seed.InvestigationID, admin)
	require.Error(t, err)

	spend, err := db.New(f.pool).GetDailySpend(t.Context(), db.GetDailySpendParams{Route: f.seed.Route, Day: dayOf(spendDayTime)})
	require.NoError(t, err)
	require.EqualValues(t, rubricCostMicros, spend.RouteMicros)

	var n int

	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM scenarios`).Scan(&n))
	require.Equal(t, 1, n)
	require.Zero(t, auditCount(t.Context(), t, f.pool, ActionDrafted, "latency-alert-checkout-api-p99-latency-above-2s-for-5m-3f6e8d1c4b70"))
}
