//go:build integration

package feedback

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

func TestSubmitEnqueuesLessonDraft(t *testing.T) {
	t.Parallel()

	finding := Target{Kind: TargetFinding, Ref: FindingRef(1, "f1")}

	tests := []struct {
		name        string
		draft       bool
		submissions []Submission
		// wantJobs is how many draft_lesson jobs wait in the end.
		wantJobs int
	}{
		{
			name:        "a reason enqueues a draft",
			draft:       true,
			submissions: []Submission{{Target: finding, Rating: output.RatingWrong, Reason: "The deploy was an hour later.", Author: webAna}},
			wantJobs:    1,
		},
		{
			name:        "a reason that redaction changes enqueues a draft",
			draft:       true,
			submissions: []Submission{{Target: finding, Rating: output.RatingWrong, Reason: "Ask ana@example.com about the deploy.", Author: slack}},
			wantJobs:    1,
		},
		{
			name:        "a rating alone does not",
			draft:       true,
			submissions: []Submission{{Target: finding, Rating: output.RatingWrong, Author: webAna}},
		},
		{
			name:        "a reason of white space does not",
			draft:       true,
			submissions: []Submission{{Target: finding, Rating: output.RatingWrong, Reason: "  \n ", Author: webAna}},
		},
		{
			name:        "nothing is enqueued without a summarize role",
			submissions: []Submission{{Target: finding, Rating: output.RatingWrong, Reason: "The deploy was an hour later.", Author: webAna}},
		},
		{
			name:  "a changed reason while the draft waits does not enqueue a second one",
			draft: true,
			submissions: []Submission{
				{Target: finding, Rating: output.RatingWrong, Reason: "The deploy was an hour later.", Author: webAna},
				{Target: finding, Rating: output.RatingWrong, Reason: "It was two hours later.", Author: webAna},
			},
			wantJobs: 1,
		},
		{
			name:  "two people with reasons enqueue two drafts",
			draft: true,
			submissions: []Submission{
				{Target: finding, Rating: output.RatingWrong, Reason: "The deploy was an hour later.", Author: webAna},
				{Target: finding, Rating: output.RatingCorrect, Reason: "The deploy was right.", Author: webBen},
			},
			wantJobs: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			var ids []string

			for _, s := range tt.submissions {
				s.InvestigationID = finalInvestigation
				s.DraftLessons = tt.draft

				got, err := submit(t, pool, s)
				require.NoError(t, err)

				if tt.draft && s.Reason != "" && len(ids) < tt.wantJobs {
					ids = append(ids, got.Feedback.ID.String())
				}
			}

			rows, err := pool.Query(t.Context(), "SELECT payload->>'feedbackId' FROM jobs WHERE kind = 'draft_lesson' ORDER BY id")
			require.NoError(t, err)

			defer rows.Close()

			var got []string

			for rows.Next() {
				var id string

				require.NoError(t, rows.Scan(&id))

				got = append(got, id)
			}

			require.NoError(t, rows.Err())
			require.Len(t, got, tt.wantJobs)
			require.Equal(t, ids[:min(len(ids), tt.wantJobs)], got, "each job names the feedback it drafts from")
		})
	}
}
