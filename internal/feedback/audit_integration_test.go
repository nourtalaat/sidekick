//go:build integration

package feedback

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

func TestConfirmationChangesAreAudited(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		clicks []click
		// newVerdict, when set, replaces the investigation's verdict after
		// the first click, as a follow-up that changes the verdict does.
		newVerdict string
		want       []auditRow
	}{
		{
			name:   "a first confirmation writes no row",
			clicks: []click{{user: "U01ANA", rating: output.RatingCorrect, stored: true, change: Confirmed}},
		},
		{
			name: "a withdrawn confirmation writes one row",
			clicks: []click{
				{user: "U01ANA", rating: output.RatingCorrect, stored: true, change: Confirmed},
				{user: "U01BEN", rating: output.RatingWrong, stored: true, change: Withdrawn},
			},
			want: []auditRow{{Actor: "user U01BEN", Action: "verdict.withdrawn", OldVerdict: "true_positive"}},
		},
		{
			name: "a revised confirmation writes one row",
			clicks: []click{
				{user: "U01ANA", rating: output.RatingCorrect, stored: true, change: Confirmed},
				{user: "U01BEN", rating: output.RatingCorrect, stored: true, change: Revised},
			},
			newVerdict: "false_positive",
			want:       []auditRow{{Actor: "user U01BEN", Action: "verdict.revised", OldVerdict: "true_positive", NewVerdict: "false_positive"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			for i, c := range tt.clicks {
				if i == 1 && tt.newVerdict != "" {
					_, err := pool.Exec(t.Context(), "UPDATE investigations SET verdict = $1 WHERE id = $2", tt.newVerdict, finalInvestigation)
					require.NoError(t, err)
				}

				res, err := rate(t, pool, VerdictRating{InvestigationID: finalInvestigation, Rating: c.rating, SlackUserID: c.user})
				require.NoError(t, err)
				require.Equal(t, Result{Stored: c.stored, Confirmation: c.change}, res, "click %d", i+1)
			}

			require.Equal(t, tt.want, auditRows(t, pool))
		})
	}
}

// auditRow is an audit log row as the tests compare it.
type auditRow struct {
	Actor      string
	Action     string
	OldVerdict string
	NewVerdict string
}

// auditRows returns the audit log of finalInvestigation, oldest first.
func auditRows(t *testing.T, pool *pgxpool.Pool) []auditRow {
	t.Helper()

	rows, err := pool.Query(t.Context(),
		"SELECT actor_kind || ' ' || actor_id, action, details FROM audit_log WHERE target_kind = 'investigation' AND target_id = $1 AND investigation_id = $2 ORDER BY id",
		finalInvestigation.String(), finalInvestigation)
	require.NoError(t, err)

	defer rows.Close()

	var out []auditRow

	for rows.Next() {
		var (
			r       auditRow
			details []byte
		)

		require.NoError(t, rows.Scan(&r.Actor, &r.Action, &details))

		var d struct {
			Old *confirmationView `json:"old"`
			New *confirmationView `json:"new"`
		}

		require.NoError(t, json.Unmarshal(details, &d))

		if d.Old != nil {
			r.OldVerdict = d.Old.Verdict
		}

		if d.New != nil {
			r.NewVerdict = d.New.Verdict
		}

		out = append(out, r)
	}

	require.NoError(t, rows.Err())

	return out
}
