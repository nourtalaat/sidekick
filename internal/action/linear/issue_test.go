package linear

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/daily"
)

const issueOps = `{"team": "OPS", "title": "Checkout p99 doubled", "description": "After the 14:02 deploy.", "labels": ["incident", "bug"]}`

var opsLimits = IssueLimits{Teams: []string{"OPS"}, Labels: []string{"incident", "Bug"}, MaxPerDay: 3}

func TestIssueValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		limits    IssueLimits
		params    string
		wantErr   string
		wantLimit bool
	}{
		{name: "allowed team and labels", limits: opsLimits, params: issueOps},
		{name: "no labels asked", limits: opsLimits, params: `{"team": "ops", "title": "T"}`},
		{name: "another team", limits: opsLimits, params: `{"team": "ENG", "title": "T"}`, wantErr: `team "ENG": outside configured limits: the team is not one of limits.teams`, wantLimit: true},
		{name: "a team ID might match a key", limits: IssueLimits{Teams: []string{opsTeamID}, MaxPerDay: 1}, params: `{"team": "OPS", "title": "T"}`},
		{name: "a label that is not allowed", limits: opsLimits, params: `{"team": "OPS", "title": "T", "labels": ["urgent"]}`, wantErr: `check the labels: label "urgent": outside configured limits: the label is not one of limits.labels`, wantLimit: true},
		{name: "no labels allowed", limits: IssueLimits{Teams: []string{"OPS"}, MaxPerDay: 1}, params: `{"team": "OPS", "title": "T", "labels": ["incident"]}`, wantErr: `check the labels: label "incident": outside configured limits: the label is not one of limits.labels`, wantLimit: true},
		{name: "too many labels", limits: opsLimits, params: `{"team": "OPS", "title": "T", "labels": ["incident","incident","incident","incident","incident","incident"]}`, wantErr: "check the labels: 6 labels, at most 5: outside configured limits: labels: too many", wantLimit: true},
		{name: "an empty title", limits: opsLimits, params: `{"team": "OPS", "title": " "}`, wantErr: "title: required"},
		{name: "a long title", limits: opsLimits, params: `{"team": "OPS", "title": "` + strings.Repeat("x", maxTitleChars+1) + `"}`, wantErr: "201 characters, at most 200: outside configured limits: title: too long", wantLimit: true},
		{name: "a long description", limits: opsLimits, params: `{"team": "OPS", "title": "T", "description": "` + strings.Repeat("x", maxDescriptionChars+1) + `"}`, wantErr: "4001 characters, at most 4000: outside configured limits: description: too long", wantLimit: true},
		{name: "no team", limits: opsLimits, params: `{"title": "T"}`, wantErr: "team: required"},
		{name: "an unknown parameter", limits: opsLimits, params: `{"team": "OPS", "title": "T", "assignee": "me"}`, wantErr: `decode: invalid parameters: json: unknown field "assignee"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, api := newServer(t)

			err := newIssue(t, api, tt.limits, allowDaily(t)).Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, "validate ops-issue: "+tt.wantErr)
			require.Equal(t, tt.wantLimit, isLimit(err))
		})
	}
}

func TestIssueExecute(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	got, err := newIssue(t, api, opsLimits, allowDaily(t)).Execute(t.Context(), invocation(issueOps))
	require.NoError(t, err)
	require.Equal(t, "Created the Linear issue OPS-1.", got.Summary)
	require.Equal(t, []action.Link{{Title: "OPS-1", URL: "https://linear.app/acme/issue/new"}}, got.Links)

	require.Len(t, f.inputs, 1)

	in := f.inputs[0]
	require.Equal(t, deriveUUID(testKey), in.ID)
	require.Equal(t, opsTeamID, in.TeamID)
	require.Equal(t, "Checkout p99 doubled", in.Title)
	require.Equal(t, []string{"l-incident", "l-ops-bug"}, in.LabelIDs, "the team's own label wins over the workspace's")
	require.Equal(t, "After the 14\\:02 deploy.\n\n---\nPosted by Sidekick after an approver approved it. [Investigation]("+
		testPublicURL+"/investigations/"+testInvestigationID+")\n\nSidekick key: "+testKey, in.Description)
}

func TestIssueExecuteEscapesTheText(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	params, err := json.Marshal(IssueParams{Team: "OPS", Title: "[x](http://evil.example.com) @bob", Description: "[click](https://evil.example.com)\n@alice"})
	require.NoError(t, err)

	_, err = newIssue(t, api, opsLimits, allowDaily(t)).Execute(t.Context(), invocation(string(params)))
	require.NoError(t, err)
	require.Len(t, f.inputs, 1)
	require.True(t, strings.HasPrefix(f.inputs[0].Description, `\[click\]\(https\://evil.example.com\)`+"\n"+`\@alice`))
	require.Equal(t, "[x](http://evil.example.com) @bob", f.inputs[0].Title, "a title is plain text in Linear")
}

// TestIssueExecuteTwiceWithTheSameKey is the crash and retry case: one
// issue exists, and the repeat is not counted against the daily limit.
func TestIssueExecuteTwiceWithTheSameKey(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	limiter := daily.NewMockLimiter(gomock.NewController(t))
	limiter.EXPECT().Check(gomock.Any(), "ops-issue", testProposalID, 3).Return(nil).Times(1)

	a := newIssue(t, api, opsLimits, limiter)

	first, err := a.Execute(t.Context(), invocation(issueOps))
	require.NoError(t, err)

	second, err := a.Execute(t.Context(), invocation(issueOps))
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, f.creates)
}

// TestIssueExecuteFindsTheIssueByItsKey covers a Linear that does not keep
// the ID the client chose: the repeat finds the issue by the key in its
// description.
func TestIssueExecuteFindsTheIssueByItsKey(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	f.ignoreClientID = true

	limiter := daily.NewMockLimiter(gomock.NewController(t))
	limiter.EXPECT().Check(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)

	a := newIssue(t, api, opsLimits, limiter)

	first, err := a.Execute(t.Context(), invocation(issueOps))
	require.NoError(t, err)

	second, err := a.Execute(t.Context(), invocation(issueOps))
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, f.creates)
}

func TestIssueExecuteAfterALostAnswer(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	f.loseAnswer = true

	got, err := newIssue(t, api, opsLimits, allowDaily(t)).Execute(t.Context(), invocation(issueOps))
	require.NoError(t, err)
	require.Equal(t, "Created the Linear issue OPS-1.", got.Summary)
	require.Equal(t, 1, f.creates)
}

func TestIssueExecuteStopsAtTheDailyLimit(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	limiter := daily.NewMockLimiter(gomock.NewController(t))
	limiter.EXPECT().Check(gomock.Any(), "ops-issue", testProposalID, 3).Return(errors.Join(action.ErrLimit, errors.New("3 of 3 uses taken today")))

	_, err := newIssue(t, api, opsLimits, limiter).Execute(t.Context(), invocation(issueOps))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Zero(t, f.creates)
}

func TestIssueExecuteRefusesWhatTheLimitsForbidNow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		limits IssueLimits
		params string
	}{
		{name: "a team that is not allowed", limits: IssueLimits{Teams: []string{"ENG"}, Labels: []string{"incident"}, MaxPerDay: 1}, params: `{"team": "OPS", "title": "T"}`},
		{name: "a team ID that names another team", limits: IssueLimits{Teams: []string{engTeamID}, MaxPerDay: 1}, params: `{"team": "OPS", "title": "T"}`},
		{name: "a label", limits: IssueLimits{Teams: []string{"OPS"}, MaxPerDay: 1}, params: `{"team": "OPS", "title": "T", "labels": ["incident"]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, api := newServer(t)

			_, err := newIssue(t, api, tt.limits, allowDaily(t)).Execute(t.Context(), invocation(tt.params))
			require.ErrorIs(t, err, action.ErrLimit)
			require.Zero(t, f.creates)
		})
	}
}

func TestIssueExecuteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		params        string
		setup         func(f *fakeLinear)
		wantPermanent bool
	}{
		{name: "rate limited", params: issueOps, setup: func(f *fakeLinear) { f.errorCode = codeRateLimited }},
		{name: "a bad gateway", params: issueOps, setup: func(f *fakeLinear) { f.status = http.StatusBadGateway }},
		{name: "not allowed", params: issueOps, setup: func(f *fakeLinear) { f.errorCode = "FORBIDDEN" }, wantPermanent: true},
		{name: "a label that does not exist", params: `{"team": "OPS", "title": "T", "labels": ["incident"]}`, setup: func(f *fakeLinear) { f.labels = nil }, wantPermanent: true},
		{name: "a team that does not exist", params: issueOps, setup: func(f *fakeLinear) { f.teams = nil }, wantPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, api := newServer(t)
			tt.setup(f)

			_, err := newIssue(t, api, opsLimits, allowDaily(t)).Execute(t.Context(), invocation(tt.params))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.NotContains(t, err.Error(), testAPIKey)
		})
	}
}

func TestIssueSpecNamesTheLimits(t *testing.T) {
	t.Parallel()

	_, api := newServer(t)
	spec := newIssue(t, api, opsLimits, allowDaily(t)).Spec()

	require.Equal(t, "ops-issue", spec.Name)
	require.Contains(t, spec.Description, "Allowed teams: OPS. Allowed labels: incident, Bug. At most 3 issues a day.")
	require.True(t, json.Valid(spec.ParamsSchema))
}

func TestIssueExecuteSaysWhoApproved(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		approvedBy string
		want       string
	}{
		{name: "an approver", approvedBy: "u1", want: "Posted by Sidekick after an approver approved it."},
		{name: "the policy of a read-write route", approvedBy: "policy", want: "Posted by Sidekick, approved by the read + write policy of its route."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, api := newServer(t)

			inv := invocation(issueOps)
			inv.ApprovedBy = tt.approvedBy

			_, err := newIssue(t, api, opsLimits, allowDaily(t)).Execute(t.Context(), inv)
			require.NoError(t, err)
			require.Len(t, f.inputs, 1)
			require.Contains(t, f.inputs[0].Description, "\n\n---\n"+tt.want)
		})
	}
}
