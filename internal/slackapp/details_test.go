package slackapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

const testPublicURL = "https://sidekick.example.com"

// formInvestigation is the investigation the forms are for: a true
// positive in its second run, with two findings and a fix.
func formInvestigation() db.Investigation {
	return db.Investigation{
		ID:      uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1"),
		Run:     2,
		Status:  "final",
		Verdict: new("true_positive"),
		Report: json.RawMessage(`{"verdict": "true_positive", "findings": [
			{"id": "f1", "claim": "Errors rose at 14:02."},
			{"id": "f2", "claim": "The deploy 4f2c1a changed the connection pool size from 20 to 5 and exhausted it."}],
			"fixes": [{"id": "x1", "title": "Roll back 4f2c1a", "detail": "Revert the deploy."}]}`),
	}
}

func TestDetailsViewGolden(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile("fixture/forms/details-wrong-with-tool-calls.json")
	require.NoError(t, err)

	got, err := json.Marshal(detailsView(formInvestigation(), detailsOptions{Rating: output.RatingWrong, PublicURL: testPublicURL}))
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
}

func TestDetailsView(t *testing.T) {
	t.Parallel()

	withFindings := func(n int) db.Investigation {
		inv := formInvestigation()

		var findings []string
		for i := range n {
			findings = append(findings, fmt.Sprintf(`{"id": "f%d", "claim": "Claim %d"}`, i, i))
		}

		inv.Report = json.RawMessage(`{"findings": [` + strings.Join(findings, ",") + `]}`)

		return inv
	}

	tests := []struct {
		name string
		inv  db.Investigation
		opts detailsOptions
		// wantParts are the values of the part options, in order.
		wantParts []string
		// wantBlocks are the block IDs, in order.
		wantBlocks []string
		// wantRating is the initially chosen rating, empty when none.
		wantRating string
	}{
		{
			name:       "without a public URL a tool call cannot be rated",
			inv:        formInvestigation(),
			wantParts:  []string{"run:2/verdict", "run:2/finding:f1", "run:2/finding:f2", "run:2/fix:x1"},
			wantBlocks: []string{blockPart, blockRating, blockReason, blockVerdict, blockCause},
		},
		{
			name:       "with a public URL",
			inv:        formInvestigation(),
			opts:       detailsOptions{PublicURL: testPublicURL + "/"},
			wantParts:  []string{"run:2/verdict", "run:2/finding:f1", "run:2/finding:f2", "run:2/fix:x1", partToolCall},
			wantBlocks: []string{blockPart, "", blockToolCall, blockRating, blockReason, blockVerdict, blockCause},
		},
		{
			name:       "Wrong is chosen after a click on Wrong",
			inv:        formInvestigation(),
			opts:       detailsOptions{Rating: output.RatingWrong},
			wantParts:  []string{"run:2/verdict", "run:2/finding:f1", "run:2/finding:f2", "run:2/fix:x1"},
			wantBlocks: []string{blockPart, blockRating, blockReason, blockVerdict, blockCause},
			wantRating: "wrong",
		},
		{
			name: "a report that cannot be read leaves the verdict",
			inv: func() db.Investigation {
				inv := formInvestigation()
				inv.Report = json.RawMessage(`not json`)

				return inv
			}(),
			wantParts:  []string{"run:2/verdict"},
			wantBlocks: []string{blockPart, blockRating, blockReason, blockVerdict, blockCause},
		},
		{
			name:       "findings are capped, and the tool call stays",
			inv:        withFindings(maxPartOptions + 10),
			opts:       detailsOptions{PublicURL: testPublicURL},
			wantParts:  nil,
			wantBlocks: []string{blockPart, "", blockToolCall, blockRating, blockReason, blockVerdict, blockCause},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			view := detailsView(tt.inv, tt.opts)
			require.Equal(t, callbackFeedbackDetails, view.CallbackID)
			require.Equal(t, tt.inv.ID.String(), view.PrivateMetadata)

			var blockIDs []string

			for _, b := range view.Blocks.BlockSet {
				blockIDs = append(blockIDs, blockIDOf(b))
			}

			require.Equal(t, tt.wantBlocks, blockIDs)

			part, ok := view.Blocks.BlockSet[0].(*slack.InputBlock).Element.(*slack.SelectBlockElement)
			require.True(t, ok)

			var values []string
			for _, o := range part.Options {
				values = append(values, o.Value)
				require.LessOrEqual(t, len([]rune(o.Text.Text)), maxOptionChars)
				require.LessOrEqual(t, len(o.Value), maxOptionValue)
			}

			if tt.wantParts == nil {
				require.Len(t, values, maxFindingsInForm+1+boolToInt(tt.opts.PublicURL != ""), "the verdict, the capped findings and the tool call")
				require.Equal(t, partToolCall, values[len(values)-1])
			} else {
				require.Equal(t, tt.wantParts, values)
			}

			require.Equal(t, values[0], part.InitialOption.Value, "the verdict is chosen first")

			var rating *slack.RadioButtonsBlockElement

			for _, b := range view.Blocks.BlockSet {
				if in, ok := b.(*slack.InputBlock); ok && in.BlockID == blockRating {
					rating = in.Element.(*slack.RadioButtonsBlockElement)
				}
			}

			require.NotNil(t, rating)

			if tt.wantRating == "" {
				require.Nil(t, rating.InitialOption)
			} else {
				require.Equal(t, tt.wantRating, rating.InitialOption.Value)
			}
		})
	}
}

func TestDetailsViewKeepsOptionTextOnOneLine(t *testing.T) {
	t.Parallel()

	inv := formInvestigation()
	inv.Report = json.RawMessage(`{"findings": [{"id": "f1", "claim": "Line one.\n\n  Line two.\tDone."}]}`)

	options := partOptions(inv, mustReport(t, inv), false)
	require.Equal(t, "Finding f1: Line one. Line two. Done.", options[1].Text.Text)
}

func TestTargetOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		part     string
		toolCall string
		want     feedback.Target
		wantOK   bool
	}{
		{name: "the verdict", part: "run:2/verdict", want: feedback.Target{Kind: "verdict", Ref: "run:2/verdict"}, wantOK: true},
		{name: "a finding", part: "run:2/finding:f1", want: feedback.Target{Kind: "finding", Ref: "run:2/finding:f1"}, wantOK: true},
		{name: "a fix", part: "run:2/fix:x1", want: feedback.Target{Kind: "fix", Ref: "run:2/fix:x1"}, wantOK: true},
		{name: "a tool call", part: partToolCall, toolCall: " toolu_01 ", want: feedback.Target{Kind: "tool_call", Ref: "toolu_01"}, wantOK: true},
		{name: "a tool call without an ID", part: partToolCall, want: feedback.Target{Kind: "tool_call"}, wantOK: true},
		{name: "nothing chosen", part: ""},
		{name: "something else", part: "run:2/other:x"},
		{name: "no run", part: "verdict"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := targetOf(tt.part, tt.toolCall)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDetailsSubmission(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	mem := memory.SyncOptions{Embed: true}

	tests := []struct {
		name   string
		values detailsValues
		want   feedback.Submission
		// wantErrors are the errors by block ID, nil when the form is fine.
		wantErrors map[string]string
	}{
		{
			name: "everything given",
			values: detailsValues{
				Part: "run:2/finding:f1", Rating: "wrong", Reason: "Not at 14:02.", Verdict: "false_positive", Cause: "A load test.",
			},
			want: feedback.Submission{
				InvestigationID: id, Target: feedback.Target{Kind: "finding", Ref: "run:2/finding:f1"}, Rating: output.RatingWrong,
				Reason: "Not at 14:02.", CorrectVerdict: "false_positive", CorrectCause: "A load test.",
				Author: feedback.Author{SlackUserID: "U01ANA"}, Memory: mem,
			},
		},
		{
			name:   "only what is needed",
			values: detailsValues{Part: "run:2/verdict", Rating: "correct"},
			want: feedback.Submission{
				InvestigationID: id, Target: feedback.Target{Kind: "verdict", Ref: "run:2/verdict"}, Rating: output.RatingCorrect,
				Author: feedback.Author{SlackUserID: "U01ANA"}, Memory: mem,
			},
		},
		{
			name:   "a tool call",
			values: detailsValues{Part: partToolCall, ToolCall: "toolu_01", Rating: "wrong"},
			want: feedback.Submission{
				InvestigationID: id, Target: feedback.Target{Kind: "tool_call", Ref: "toolu_01"}, Rating: output.RatingWrong,
				Author: feedback.Author{SlackUserID: "U01ANA"}, Memory: mem,
			},
		},
		{
			name:       "a tool call without its ID",
			values:     detailsValues{Part: partToolCall, Rating: "wrong"},
			wantErrors: map[string]string{blockToolCall: errorNoToolCall},
		},
		{
			name:       "a part the form does not know",
			values:     detailsValues{Part: "elsewhere", Rating: "wrong"},
			wantErrors: map[string]string{blockPart: errorOutOfDate},
		},
		{
			name:       "no rating",
			values:     detailsValues{Part: "run:2/verdict"},
			wantErrors: map[string]string{blockRating: errorNoRating},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, errs := tt.values.submission(id, "U01ANA", mem)
			require.Equal(t, tt.wantErrors, errs)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestReadDetails(t *testing.T) {
	t.Parallel()

	state := &slack.ViewState{Values: map[string]map[string]slack.BlockAction{
		blockPart:     {actionPart: {SelectedOption: slack.OptionBlockObject{Value: "run:2/fix:x1"}}},
		blockToolCall: {actionToolCall: {Value: "toolu_01"}},
		blockRating:   {actionRating: {SelectedOption: slack.OptionBlockObject{Value: "wrong"}}},
		blockReason:   {actionReason: {Value: "Why not."}},
		blockVerdict:  {actionVerdict: {SelectedOption: slack.OptionBlockObject{Value: "flapping"}}},
		blockCause:    {actionCause: {Value: "A flaky probe."}},
	}}

	require.Equal(t,
		detailsValues{Part: "run:2/fix:x1", ToolCall: "toolu_01", Rating: "wrong", Reason: "Why not.", Verdict: "flapping", Cause: "A flaky probe."},
		readDetails(state))
	require.Equal(t, detailsValues{}, readDetails(nil), "a form with no state")
	require.Equal(t, detailsValues{}, readDetails(&slack.ViewState{}), "a form with no values")
}

func TestSubmissionErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target feedback.Target
		err    error
		want   map[string]string
	}{
		{name: "invalid fields", err: &feedback.InvalidError{Detail: "The reason is longer than 2000 characters"}, want: map[string]string{blockPart: "The reason is longer than 2000 characters."}},
		{name: "no investigation", err: fmt.Errorf("lock: %w", feedback.ErrNotFound), want: map[string]string{blockPart: errorGone}},
		{name: "no verdict", err: fmt.Errorf("lock: %w", feedback.ErrNotRatable), want: map[string]string{blockPart: errorNoVerdict}},
		{name: "a real verdict that does not fit", err: fmt.Errorf("check: %w", feedback.ErrBadVerdict), want: map[string]string{blockVerdict: errorBadVerdict}},
		{
			name: "an unknown tool call", target: feedback.Target{Kind: feedback.TargetToolCall}, err: fmt.Errorf("check: %w", feedback.ErrBadTarget),
			want: map[string]string{blockToolCall: errorUnknownCall},
		},
		{
			name: "a part that is gone", target: feedback.Target{Kind: feedback.TargetFinding}, err: fmt.Errorf("check: %w", feedback.ErrBadTarget),
			want: map[string]string{blockPart: errorOutOfDate},
		},
		{name: "another error", err: errors.New("the database is down")},
		{name: "no error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, submissionErrors(tt.target, tt.err))
		})
	}
}

func TestErrorsResponse(t *testing.T) {
	t.Parallel()

	body, err := errorsResponse(map[string]string{blockToolCall: errorNoToolCall})
	require.NoError(t, err)
	require.JSONEq(t, `{"response_action": "errors", "errors": {"fb_tool_call": "Enter the ID of the tool call."}}`, string(body))
}

// blockIDOf returns the block ID of b, empty for a block with none.
func blockIDOf(b slack.Block) string {
	if in, ok := b.(*slack.InputBlock); ok {
		return in.BlockID
	}

	return ""
}

// mustReport reads the report of inv.
func mustReport(t *testing.T, inv db.Investigation) report.Report {
	t.Helper()

	return reportOf(inv)
}

// boolToInt is 1 for true and 0 for false.
func boolToInt(b bool) int {
	if b {
		return 1
	}

	return 0
}
