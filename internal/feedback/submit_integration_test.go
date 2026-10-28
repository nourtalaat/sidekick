//go:build integration

package feedback

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

// The authors of the submissions.
var (
	webAna = Author{UserID: uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e1")}
	webBen = Author{UserID: uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e2")}
	slack  = Author{SlackUserID: "U01SLACK"}
)

// feedbackRow is a stored row as the tests compare it.
type feedbackRow struct {
	Kind, Ref, Rating, Reason, RealVerdict, RealCause, Channel string
}

func TestSubmitStoresOneOpinionPerAuthorAndTarget(t *testing.T) {
	t.Parallel()

	verdict := Target{Kind: TargetVerdict, Ref: VerdictRef(1)}
	finding := Target{Kind: TargetFinding, Ref: FindingRef(1, "f1")}

	tests := []struct {
		name        string
		submissions []Submission
		// changed is RatingChanged of each submission.
		changed []bool
		want    []feedbackRow
		// confirms is whether the submissions confirm the investigation.
		confirms bool
	}{
		{
			name: "a rating of the verdict by a user",
			submissions: []Submission{
				{Target: verdict, Rating: output.RatingCorrect, Author: webAna},
			},
			changed:  []bool{true},
			want:     []feedbackRow{{Kind: "verdict", Ref: "run:1/verdict", Rating: "correct", Channel: "ui"}},
			confirms: true,
		},
		{
			name: "a wrong finding with a reason by a Slack user",
			submissions: []Submission{
				{Target: finding, Rating: output.RatingWrong, Reason: "The deploy was an hour later.", Author: slack},
			},
			changed: []bool{true},
			want: []feedbackRow{
				{Kind: "finding", Ref: "run:1/finding:f1", Rating: "wrong", Reason: "The deploy was an hour later.", Channel: "slack"},
			},
		},
		{
			name: "a new submission replaces the author's earlier one",
			submissions: []Submission{
				{Target: finding, Rating: output.RatingWrong, Reason: "Not sure.", Author: webAna},
				{Target: finding, Rating: output.RatingCorrect, Reason: "It was right after all.", Author: webAna},
			},
			changed: []bool{true, true},
			want: []feedbackRow{
				{Kind: "finding", Ref: "run:1/finding:f1", Rating: "correct", Reason: "It was right after all.", Channel: "ui"},
			},
		},
		{
			name: "the same rating with new details is not a changed rating",
			submissions: []Submission{
				{Target: finding, Rating: output.RatingWrong, Author: slack},
				{Target: finding, Rating: output.RatingWrong, Reason: "Added later.", Author: slack},
			},
			changed: []bool{true, false},
			want:    []feedbackRow{{Kind: "finding", Ref: "run:1/finding:f1", Rating: "wrong", Reason: "Added later.", Channel: "slack"}},
		},
		{
			name: "two users rate the same part",
			submissions: []Submission{
				{Target: finding, Rating: output.RatingCorrect, Author: webAna},
				{Target: finding, Rating: output.RatingWrong, Author: webBen},
			},
			changed: []bool{true, true},
			want: []feedbackRow{
				{Kind: "finding", Ref: "run:1/finding:f1", Rating: "correct", Channel: "ui"},
				{Kind: "finding", Ref: "run:1/finding:f1", Rating: "wrong", Channel: "ui"},
			},
		},
		{
			name: "a fix",
			submissions: []Submission{
				{Target: Target{Kind: TargetFix, Ref: FixRef(1, "x1")}, Rating: output.RatingWrong, Author: webAna},
			},
			changed: []bool{true},
			want:    []feedbackRow{{Kind: "fix", Ref: "run:1/fix:x1", Rating: "wrong", Channel: "ui"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			for i, s := range tt.submissions {
				s.InvestigationID = finalInvestigation

				got, err := submit(t, pool, s)
				require.NoError(t, err)
				require.Equal(t, tt.changed[i], got.RatingChanged, "submission %d", i+1)
			}

			require.Equal(t, tt.want, feedbackRows(t, pool, finalInvestigation))

			confirmed := investigation(t, pool, finalInvestigation).ConfirmedVerdict
			if tt.confirms {
				require.NotNil(t, confirmed)
			} else {
				require.Nil(t, confirmed, "only a verdict rating or a real verdict confirms")
			}
		})
	}
}

func TestSubmitConfirms(t *testing.T) {
	t.Parallel()

	verdict := Target{Kind: TargetVerdict, Ref: VerdictRef(1)}

	tests := []struct {
		name        string
		submissions []Submission
		// wantChanges are the Confirmation of each submission.
		wantChanges []Change
		// wantVerdict and wantCause are the confirmed values at the end,
		// empty when nothing is confirmed.
		wantVerdict, wantCause string
		// wantBy is who confirmed it at the end: a user, or a Slack user.
		wantBy Author
		// wantAudit are the audit rows, oldest first.
		wantAudit []auditRow
		// wantMemory is the memory entry's verdict at the end, empty when
		// the investigation is not in memory.
		wantMemory string
	}{
		{
			name:        "a Correct on the verdict confirms it without an audit row",
			submissions: []Submission{{Target: verdict, Rating: output.RatingCorrect, Author: webAna}},
			wantChanges: []Change{Confirmed},
			wantVerdict: "true_positive", wantCause: "Deploy 4f2c1a", wantBy: webAna,
			wantMemory: "true_positive",
		},
		{
			name: "a real verdict confirms it and is audited",
			submissions: []Submission{
				{Target: verdict, Rating: output.RatingWrong, CorrectVerdict: "false_positive", CorrectCause: "A load test.", Author: webAna},
			},
			wantChanges: []Change{Confirmed},
			wantVerdict: "false_positive", wantCause: "A load test.", wantBy: webAna,
			wantAudit:  []auditRow{{Actor: "user " + webAna.UserID.String(), Action: "verdict.confirmed", NewVerdict: "false_positive"}},
			wantMemory: "false_positive",
		},
		{
			name: "a real verdict on a finding confirms it too",
			submissions: []Submission{
				{Target: Target{Kind: TargetFinding, Ref: FindingRef(1, "f1")}, Rating: output.RatingWrong, CorrectVerdict: "inconclusive", Author: slack},
			},
			wantChanges: []Change{Confirmed},
			wantVerdict: "inconclusive", wantBy: slack,
			wantAudit:  []auditRow{{Actor: "user U01SLACK", Action: "verdict.confirmed", NewVerdict: "inconclusive"}},
			wantMemory: "inconclusive",
		},
		{
			name: "the reported verdict as the real one keeps the report's cause",
			submissions: []Submission{
				{Target: Target{Kind: TargetFix, Ref: FixRef(1, "x1")}, Rating: output.RatingCorrect, CorrectVerdict: "true_positive", Author: webAna},
			},
			wantChanges: []Change{Confirmed},
			wantVerdict: "true_positive", wantCause: "Deploy 4f2c1a", wantBy: webAna,
			wantAudit:  []auditRow{{Actor: "user " + webAna.UserID.String(), Action: "verdict.confirmed", NewVerdict: "true_positive"}},
			wantMemory: "true_positive",
		},
		{
			name: "a later real verdict that disagrees revises it and is audited",
			submissions: []Submission{
				{Target: verdict, Rating: output.RatingCorrect, Author: slack},
				{Target: verdict, Rating: output.RatingWrong, CorrectVerdict: "false_positive", Author: webBen},
			},
			wantChanges: []Change{Confirmed, Revised},
			wantVerdict: "false_positive", wantBy: webBen,
			wantAudit: []auditRow{
				{Actor: "user " + webBen.UserID.String(), Action: "verdict.revised", OldVerdict: "true_positive", NewVerdict: "false_positive"},
			},
			wantMemory: "false_positive",
		},
		{
			name: "a Wrong takes back the confirmation",
			submissions: []Submission{
				{Target: verdict, Rating: output.RatingCorrect, Author: webAna},
				{Target: verdict, Rating: output.RatingWrong, Author: webBen},
			},
			wantChanges: []Change{Confirmed, Withdrawn},
			wantAudit: []auditRow{
				{Actor: "user " + webBen.UserID.String(), Action: "verdict.withdrawn", OldVerdict: "true_positive"},
			},
		},
		{
			name: "a rating of a fix does not confirm",
			submissions: []Submission{
				{Target: Target{Kind: TargetFix, Ref: FixRef(1, "x1")}, Rating: output.RatingCorrect, Author: webAna},
			},
			wantChanges: []Change{Unchanged},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			for i, s := range tt.submissions {
				s.InvestigationID = finalInvestigation

				got, err := submit(t, pool, s)
				require.NoError(t, err)
				require.Equal(t, tt.wantChanges[i], got.Confirmation, "submission %d", i+1)
			}

			inv := investigation(t, pool, finalInvestigation)
			require.Equal(t, tt.wantVerdict, deref(inv.ConfirmedVerdict))
			require.Equal(t, tt.wantCause, deref(inv.ConfirmedCause))

			if tt.wantVerdict != "" {
				require.NotNil(t, inv.ConfirmedAt)

				if tt.wantBy.SlackUserID != "" {
					require.Equal(t, tt.wantBy.SlackUserID, deref(inv.ConfirmedBySlackUser))
					require.Nil(t, inv.ConfirmedBy)
				} else {
					require.Equal(t, &tt.wantBy.UserID, inv.ConfirmedBy)
					require.Nil(t, inv.ConfirmedBySlackUser)
				}
			}

			require.Equal(t, tt.wantAudit, auditRows(t, pool))

			entry := memoryEntry(t, pool, finalInvestigation)
			if tt.wantMemory == "" {
				require.Nil(t, entry)
			} else {
				require.NotNil(t, entry)
				require.Equal(t, tt.wantMemory, entry.verdict)
			}
		})
	}
}

func TestSubmitRefuses(t *testing.T) {
	t.Parallel()

	unknown := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000ff")

	tests := []struct {
		name string
		s    Submission
		// wantErr is the error that is returned.
		wantErr error
	}{
		{
			name:    "an investigation that does not exist",
			s:       Submission{InvestigationID: unknown, Target: Target{Kind: TargetToolCall, Ref: "call-1"}, Rating: output.RatingWrong, Author: webAna},
			wantErr: ErrNotFound,
		},
		{
			name:    "the verdict of a running investigation",
			s:       Submission{InvestigationID: runningInvestigation, Target: Target{Kind: TargetVerdict, Ref: VerdictRef(1)}, Rating: output.RatingWrong, Author: webAna},
			wantErr: ErrNotRatable,
		},
		{
			name:    "a finding of a running investigation",
			s:       Submission{InvestigationID: runningInvestigation, Target: Target{Kind: TargetFinding, Ref: FindingRef(1, "f1")}, Rating: output.RatingWrong, Author: webAna},
			wantErr: ErrNotRatable,
		},
		{
			name: "a real verdict on a running investigation",
			s: Submission{
				InvestigationID: runningInvestigation, Target: Target{Kind: TargetToolCall, Ref: "call-1"}, Rating: output.RatingWrong,
				CorrectVerdict: "false_positive", Author: webAna,
			},
			wantErr: ErrNotRatable,
		},
		{
			name:    "a verdict of another run",
			s:       Submission{InvestigationID: finalInvestigation, Target: Target{Kind: TargetVerdict, Ref: VerdictRef(2)}, Rating: output.RatingWrong, Author: webAna},
			wantErr: ErrBadTarget,
		},
		{
			name:    "a finding that is not in the report",
			s:       Submission{InvestigationID: finalInvestigation, Target: Target{Kind: TargetFinding, Ref: FindingRef(1, "f9")}, Rating: output.RatingWrong, Author: webAna},
			wantErr: ErrBadTarget,
		},
		{
			name:    "a finding of another run",
			s:       Submission{InvestigationID: finalInvestigation, Target: Target{Kind: TargetFinding, Ref: FindingRef(2, "f1")}, Rating: output.RatingWrong, Author: webAna},
			wantErr: ErrBadTarget,
		},
		{
			name:    "a fix that is not in the report",
			s:       Submission{InvestigationID: finalInvestigation, Target: Target{Kind: TargetFix, Ref: FixRef(1, "f1")}, Rating: output.RatingWrong, Author: webAna},
			wantErr: ErrBadTarget,
		},
		{
			name:    "a tool call that is not in the steps",
			s:       Submission{InvestigationID: runningInvestigation, Target: Target{Kind: TargetToolCall, Ref: "call-9"}, Rating: output.RatingWrong, Author: webAna},
			wantErr: ErrBadTarget,
		},
		{
			name: "a real verdict that is not a verdict",
			s: Submission{
				InvestigationID: finalInvestigation, Target: Target{Kind: TargetVerdict, Ref: VerdictRef(1)}, Rating: output.RatingWrong,
				CorrectVerdict: "maybe", Author: webAna,
			},
			wantErr: ErrBadVerdict,
		},
		{
			name: "Correct with another real verdict than the reported one",
			s: Submission{
				InvestigationID: finalInvestigation, Target: Target{Kind: TargetVerdict, Ref: VerdictRef(1)}, Rating: output.RatingCorrect,
				CorrectVerdict: "false_positive", Author: webAna,
			},
			wantErr: ErrBadVerdict,
		},
		{
			name: "Wrong with the reported verdict as the real one",
			s: Submission{
				InvestigationID: finalInvestigation, Target: Target{Kind: TargetVerdict, Ref: VerdictRef(1)}, Rating: output.RatingWrong,
				CorrectVerdict: "true_positive", Author: webAna,
			},
			wantErr: ErrBadVerdict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			_, err := submit(t, pool, tt.s)
			require.ErrorIs(t, err, tt.wantErr)

			require.Empty(t, feedbackRows(t, pool, finalInvestigation), "nothing is stored")
			require.Empty(t, feedbackRows(t, pool, runningInvestigation), "nothing is stored")
			require.Nil(t, investigation(t, pool, finalInvestigation).ConfirmedVerdict)
		})
	}
}

func TestSubmitRatesAToolCallWhileTheInvestigationRuns(t *testing.T) {
	t.Parallel()

	pool := setup(t)

	got, err := submit(t, pool, Submission{
		InvestigationID: runningInvestigation, Target: Target{Kind: TargetToolCall, Ref: " call-1 "}, Rating: output.RatingWrong,
		Reason: "It looked at the wrong service.", Author: webAna,
	})
	require.NoError(t, err)
	require.Equal(t, Change(""), got.Confirmation)
	require.Equal(t, []feedbackRow{
		{Kind: "tool_call", Ref: "call-1", Rating: "wrong", Reason: "It looked at the wrong service.", Channel: "ui"},
	}, feedbackRows(t, pool, runningInvestigation))
}

// submit submits s in a transaction of its own.
func submit(t *testing.T, pool *pgxpool.Pool, s Submission) (Submitted, error) {
	t.Helper()

	var got Submitted

	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		var err error

		got, err = Submit(t.Context(), tx, s)
		if err != nil {
			return fmt.Errorf("submit: %w", err)
		}

		return nil
	})
	if err != nil {
		return Submitted{}, fmt.Errorf("submit in a transaction: %w", err)
	}

	return got, nil
}

// feedbackRows returns the feedback rows of the investigation with id,
// oldest first.
func feedbackRows(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) []feedbackRow {
	t.Helper()

	rows, err := pool.Query(t.Context(),
		`SELECT target_kind, target_ref, rating, coalesce(reason, ''), coalesce(correct_verdict, ''), coalesce(correct_cause, ''), channel
		 FROM feedback WHERE investigation_id = $1 ORDER BY created_at, id`, id)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (feedbackRow, error) {
		var r feedbackRow

		if err := row.Scan(&r.Kind, &r.Ref, &r.Rating, &r.Reason, &r.RealVerdict, &r.RealCause, &r.Channel); err != nil {
			return feedbackRow{}, fmt.Errorf("scan a feedback row: %w", err)
		}

		return r, nil
	})
	require.NoError(t, err)

	return out
}
