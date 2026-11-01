//go:build integration

package lessons

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// checkViolation is the Postgres error code check_violation.
const checkViolation = "23514"

func TestLessonConstraints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		origin string
		status string
		text   string
		scope  [4]*string
		// wantViolation is set when the database must refuse the lesson.
		wantViolation bool
	}{
		{name: "a drafted lesson with a service", origin: "feedback", status: "proposed", text: "Check the deploy.", scope: [4]*string{new("checkout-api")}},
		{name: "a drafted lesson with only a strategy", origin: "feedback", status: "proposed", text: "Check the deploy.", scope: [4]*string{nil, nil, nil, new("latency-alert")}},
		{name: "a drafted lesson with no scope", origin: "feedback", status: "proposed", text: "Check the deploy.", wantViolation: true},
		{name: "a global lesson from the web UI", origin: "ui", status: "active", text: "Check the deploy."},
		{name: "a global lesson from Slack", origin: "slack", status: "active", text: "Check the deploy."},
		{name: "a global lesson from a file", origin: "file", status: "active", text: "Check the deploy."},
		{name: "a blank scope field", origin: "ui", status: "active", text: "Check the deploy.", scope: [4]*string{new(" ")}, wantViolation: true},
		{name: "an unknown origin", origin: "model", status: "active", text: "Check the deploy.", scope: [4]*string{new("checkout-api")}, wantViolation: true},
		{name: "an unknown status", origin: "ui", status: "archived", text: "Check the deploy.", wantViolation: true},
		{name: "text of 400 characters", origin: "ui", status: "active", text: strings.Repeat("a", 400)},
		{name: "text of 401 characters", origin: "ui", status: "active", text: strings.Repeat("a", 401), wantViolation: true},
		{name: "empty text", origin: "ui", status: "active", text: "", wantViolation: true},
		{name: "blank text", origin: "ui", status: "active", text: "   ", wantViolation: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			_, err := pool.Exec(t.Context(), `
				INSERT INTO lessons (id, text, scope_service, scope_alert, scope_route, scope_strategy, status, origin)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				uuid.New(), tt.text, tt.scope[0], tt.scope[1], tt.scope[2], tt.scope[3], tt.status, tt.origin)

			if !tt.wantViolation {
				require.NoError(t, err)

				return
			}

			pgErr, ok := errors.AsType[*pgconn.PgError](err)
			require.True(t, ok, "want a check violation, got %v", err)
			require.Equal(t, checkViolation, pgErr.Code)
		})
	}
}

func TestRatingCounts(t *testing.T) {
	t.Parallel()

	ana, ben := uuid.New(), uuid.New()

	tests := []struct {
		name string
		// steps are run in order against the lesson, which only the final
		// investigation used.
		steps         []ratingStep
		wantHelpful   int
		wantUnhelpful int
	}{
		{
			name:        "a rating of correct is helpful",
			steps:       []ratingStep{{investigation: finalInvestigation, user: ana, rating: "correct"}},
			wantHelpful: 1,
		},
		{
			name:          "a rating of wrong is unhelpful",
			steps:         []ratingStep{{investigation: finalInvestigation, user: ana, rating: "wrong"}},
			wantUnhelpful: 1,
		},
		{
			name: "two people count twice",
			steps: []ratingStep{
				{investigation: finalInvestigation, user: ana, rating: "correct"},
				{investigation: finalInvestigation, user: ben, rating: "wrong"},
			},
			wantHelpful:   1,
			wantUnhelpful: 1,
		},
		{
			name: "changing a rating moves the count",
			steps: []ratingStep{
				{investigation: finalInvestigation, user: ana, rating: "correct"},
				{investigation: finalInvestigation, user: ana, rating: "wrong"},
			},
			wantUnhelpful: 1,
		},
		{
			name: "the same rating again changes nothing",
			steps: []ratingStep{
				{investigation: finalInvestigation, user: ana, rating: "correct"},
				{investigation: finalInvestigation, user: ana, rating: "correct"},
			},
			wantHelpful: 1,
		},
		{
			name:  "a rating of another part is left out",
			steps: []ratingStep{{investigation: finalInvestigation, user: ana, rating: "wrong", targetKind: "finding", targetRef: "run:1/finding:f1"}},
		},
		{
			name:  "an investigation that did not use the lesson is left out",
			steps: []ratingStep{{investigation: runningInvestigation, user: ana, rating: "wrong"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			lesson := insertActiveLesson(t, pool)

			_, err := pool.Exec(t.Context(), "INSERT INTO lesson_uses (lesson_id, investigation_id) VALUES ($1, $2)", lesson, finalInvestigation)
			require.NoError(t, err)

			for _, s := range tt.steps {
				s.rate(t, pool)
			}

			var helpful, unhelpful int

			require.NoError(t, pool.QueryRow(t.Context(), "SELECT helpful, unhelpful FROM lessons WHERE id = $1", lesson).Scan(&helpful, &unhelpful))
			require.Equal(t, tt.wantHelpful, helpful)
			require.Equal(t, tt.wantUnhelpful, unhelpful)
		})
	}
}

// ratingStep is one person rating one part of an investigation, which
// replaces their earlier rating of the same part.
type ratingStep struct {
	investigation uuid.UUID
	user          uuid.UUID
	rating        string
	// targetKind and targetRef name the part; they default to the verdict
	// of run 1.
	targetKind, targetRef string
}

// rate stores the rating the way the feedback queries do: one row per
// person and part, updated in place.
func (s ratingStep) rate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	kind, ref := s.targetKind, s.targetRef
	if kind == "" {
		kind, ref = "verdict", "run:1/verdict"
	}

	_, err := pool.Exec(t.Context(), `
		INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, user_id, channel, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'ui', now(), now())
		ON CONFLICT (investigation_id, target_kind, target_ref, user_id) WHERE user_id IS NOT NULL
		DO UPDATE SET rating = excluded.rating, updated_at = excluded.updated_at`,
		uuid.New(), s.investigation, kind, ref, s.rating, s.user)
	require.NoError(t, err)
}

// insertActiveLesson stores a global lesson and returns its ID.
func insertActiveLesson(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()

	id := uuid.New()

	_, err := pool.Exec(t.Context(), `
		INSERT INTO lessons (id, text, status, origin) VALUES ($1, 'Check the deploy first.', 'active', 'ui')`, id)
	require.NoError(t, err)

	return id
}
