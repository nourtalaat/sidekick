package feedback

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

func TestSubmissionNormalized(t *testing.T) {
	t.Parallel()

	user := Author{UserID: uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e1")}

	valid := Submission{Target: Target{Kind: TargetVerdict, Ref: "run:1/verdict"}, Rating: output.RatingWrong, Author: user}

	with := func(change func(s *Submission)) Submission {
		s := valid
		change(&s)

		return s
	}

	tests := []struct {
		name string
		s    Submission
		// wantDetail is the Detail of the *InvalidError, empty when s is fine.
		wantDetail string
	}{
		{name: "a rating of the verdict", s: valid},
		{name: "a Slack user", s: with(func(s *Submission) { s.Author = Author{SlackUserID: "U01ANA"} })},
		{name: "a tool call", s: with(func(s *Submission) { s.Target = Target{Kind: TargetToolCall, Ref: "call-1"} })},
		{name: "an unknown rating", s: with(func(s *Submission) { s.Rating = "maybe" }), wantDetail: "The rating must be correct or wrong"},
		{name: "no rating", s: with(func(s *Submission) { s.Rating = "" }), wantDetail: "The rating must be correct or wrong"},
		{
			name:       "an unknown kind",
			s:          with(func(s *Submission) { s.Target.Kind = "investigation" }),
			wantDetail: "The target kind must be verdict, finding, fix or tool_call",
		},
		{name: "no ref", s: with(func(s *Submission) { s.Target.Ref = "  " }), wantDetail: "The target ref is required"},
		{
			name:       "a long ref",
			s:          with(func(s *Submission) { s.Target.Ref = strings.Repeat("x", MaxToolCallChars+1) }),
			wantDetail: "The target ref is longer than 200 characters",
		},
		{name: "a reason of the longest size", s: with(func(s *Submission) { s.Reason = strings.Repeat("é", MaxReasonChars) })},
		{
			name:       "a long reason",
			s:          with(func(s *Submission) { s.Reason = strings.Repeat("é", MaxReasonChars+1) }),
			wantDetail: "The reason is longer than 2000 characters",
		},
		{
			name:       "a long cause",
			s:          with(func(s *Submission) { s.CorrectCause = strings.Repeat("x", MaxCauseChars+1) }),
			wantDetail: "The real cause is longer than 2000 characters",
		},
		{name: "no author", s: with(func(s *Submission) { s.Author = Author{} }), wantDetail: "The author is a user or a Slack user"},
		{
			name:       "two authors",
			s:          with(func(s *Submission) { s.Author.SlackUserID = "U01ANA" }),
			wantDetail: "The author is a user or a Slack user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tt.s.normalized()
			if tt.wantDetail == "" {
				require.NoError(t, err)

				return
			}

			var invalid *InvalidError

			require.ErrorAs(t, err, &invalid)
			require.Equal(t, tt.wantDetail, invalid.Detail)
		})
	}
}

func TestSubmissionNormalizedTrimsText(t *testing.T) {
	t.Parallel()

	s := Submission{
		Target: Target{Kind: TargetToolCall, Ref: " call-1 "}, Rating: output.RatingCorrect,
		Reason: "\n  because  ", CorrectCause: " Deploy ", Author: Author{SlackUserID: "U01ANA"},
	}

	got, err := s.normalized()
	require.NoError(t, err)
	require.Equal(t, "call-1", got.Target.Ref)
	require.Equal(t, "because", got.Reason)
	require.Equal(t, "Deploy", got.CorrectCause)
}

func TestTargetRefs(t *testing.T) {
	t.Parallel()

	require.Equal(t, "run:2/finding:f3", FindingRef(2, "f3"))
	require.Equal(t, "run:2/fix:x1", FixRef(2, "x1"))
}
