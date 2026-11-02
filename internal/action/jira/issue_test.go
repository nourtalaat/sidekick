package jira

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

const issueOps = `{"project": "OPS", "issueType": "task", "summary": "Checkout p99\ndoubled", "description": "After the 14:02 deploy.", "labels": ["incident"]}`

var opsLimits = IssueLimits{Projects: []string{"OPS"}, IssueTypes: []string{"Task", "Bug"}, Labels: []string{"incident", "Prod"}, MaxPerDay: 3}

func TestIssueValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    string
		wantErr   string
		wantLimit bool
	}{
		{name: "allowed project, type and labels", params: issueOps},
		{name: "no labels asked", params: `{"project": "ops", "issueType": "Bug", "summary": "S"}`},
		{name: "another project", params: `{"project": "ENG", "issueType": "Task", "summary": "S"}`, wantErr: `project "ENG": outside configured limits: the project is not one of limits.projects`, wantLimit: true},
		{name: "not a project key", params: `{"project": "ops project", "issueType": "Task", "summary": "S"}`, wantErr: "project: expected a project key such as OPS"},
		{name: "another issue type", params: `{"project": "OPS", "issueType": "Epic", "summary": "S"}`, wantErr: `issue type "Epic": outside configured limits: the issue type is not one of limits.issueTypes`, wantLimit: true},
		{name: "a label that is not allowed", params: `{"project": "OPS", "issueType": "Task", "summary": "S", "labels": ["urgent"]}`, wantErr: `label "urgent": outside configured limits: the label is not one of limits.labels`, wantLimit: true},
		{name: "labels are case sensitive", params: `{"project": "OPS", "issueType": "Task", "summary": "S", "labels": ["INCIDENT"]}`, wantErr: `label "INCIDENT": outside configured limits: the label is not one of limits.labels`, wantLimit: true},
		{name: "a label with a space", params: `{"project": "OPS", "issueType": "Task", "summary": "S", "labels": ["in cident"]}`, wantErr: "labels: a label must have no spaces"},
		{name: "too many labels", params: `{"project": "OPS", "issueType": "Task", "summary": "S", "labels": ["incident","incident","incident","incident","incident","incident"]}`, wantErr: "6 labels, at most 5: outside configured limits: labels: too many", wantLimit: true},
		{name: "an empty summary", params: `{"project": "OPS", "issueType": "Task", "summary": " \n "}`, wantErr: "summary: required"},
		{name: "a long summary", params: `{"project": "OPS", "issueType": "Task", "summary": "` + strings.Repeat("x", maxSummaryChars+1) + `"}`, wantErr: "201 characters, at most 200: outside configured limits: summary: too long", wantLimit: true},
		{name: "a long description", params: `{"project": "OPS", "issueType": "Task", "summary": "S", "description": "` + strings.Repeat("x", maxDescriptionChars+1) + `"}`, wantErr: "4001 characters, at most 4000: outside configured limits: description: too long", wantLimit: true},
		{name: "an unknown parameter", params: `{"project": "OPS", "issueType": "Task", "summary": "S", "assignee": "me"}`, wantErr: `decode the parameters: json: unknown field "assignee"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, api := newServer(t)

			err := newIssue(t, api, opsLimits, allowDaily(t)).Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
			require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
		})
	}
}

func TestIssueExecute(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	got, err := newIssue(t, api, opsLimits, allowDaily(t)).Execute(t.Context(), invocation(issueOps))
	require.NoError(t, err)
	require.Equal(t, "Created the Jira issue OPS-1.", got.Summary)
	require.Equal(t, []action.Link{{Title: "OPS-1", URL: api.URL() + "/browse/OPS-1"}}, got.Links)

	require.Len(t, f.issues, 1)

	var fields struct {
		Project     struct{ Key string }  `json:"project"`
		IssueType   struct{ Name string } `json:"issuetype"`
		Summary     string                `json:"summary"`
		Description json.RawMessage       `json:"description"`
		Labels      []string              `json:"labels"`
	}

	require.NoError(t, json.Unmarshal(f.issues[0].Fields, &fields))
	require.Equal(t, "OPS", fields.Project.Key)
	require.Equal(t, "Task", fields.IssueType.Name, "the issue type is spelled as the config spells it")
	require.Equal(t, "Checkout p99 doubled", fields.Summary, "a summary is one line")
	require.Equal(t, []string{"incident", labelPrefix + keyHash(testKey)}, fields.Labels)
	require.Equal(t, "After the 14:02 deploy.|"+footerText+"| |Investigation", bodyText(t, fields.Description))
}

func TestIssueExecuteKeepsTheTextPlain(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	params, err := json.Marshal(IssueParams{
		Project: "OPS", IssueType: "Task", Summary: "[~admin] {code}", Description: "[click|http://evil.example.com] *bold* {noformat}",
	})
	require.NoError(t, err)

	_, err = newIssue(t, api, opsLimits, allowDaily(t)).Execute(t.Context(), invocation(string(params)))
	require.NoError(t, err)

	var fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
	}

	require.NoError(t, json.Unmarshal(f.issues[0].Fields, &fields))
	require.Equal(t, "[~admin] {code}", fields.Summary)
	require.Contains(t, bodyText(t, fields.Description), "[click|http://evil.example.com] *bold* {noformat}|")
	require.Equal(t, 1, strings.Count(string(fields.Description), `"marks"`), "only the link to the investigation")
}

// TestIssueExecuteTwiceWithTheSameKey is the crash and retry case: one issue
// exists, and the repeat is not counted against the daily limit.
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

func TestIssueExecuteAfterALostAnswer(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	f.loseAnswer = true

	a := newIssue(t, api, opsLimits, allowDaily(t))

	_, err := a.Execute(t.Context(), invocation(issueOps))
	require.Error(t, err)
	require.False(t, isPermanent(err))

	got, err := a.Execute(t.Context(), invocation(issueOps))
	require.NoError(t, err)
	require.Equal(t, "Created the Jira issue OPS-1.", got.Summary)
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

	f, api := newServer(t)

	limits := IssueLimits{Projects: []string{"ENG"}, IssueTypes: []string{"Task"}, MaxPerDay: 1}

	_, err := newIssue(t, api, limits, allowDaily(t)).Execute(t.Context(), invocation(issueOps))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Zero(t, f.calls, "nothing is sent")
}

func TestIssueExecuteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		wantPermanent bool
	}{
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "a bad gateway", status: http.StatusBadGateway},
		{name: "a bad token", status: http.StatusUnauthorized, wantPermanent: true},
		{name: "no rights", status: http.StatusForbidden, wantPermanent: true},
		{name: "a field Jira refuses", status: http.StatusBadRequest, wantPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, api := newServer(t)
			f.status = tt.status

			_, err := newIssue(t, api, opsLimits, allowDaily(t)).Execute(t.Context(), invocation(issueOps))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.NotContains(t, err.Error(), testToken)
		})
	}
}

func TestIssueSpecNamesTheLimits(t *testing.T) {
	t.Parallel()

	_, api := newServer(t)
	spec := newIssue(t, api, opsLimits, allowDaily(t)).Spec()

	require.Equal(t, "ops-issue", spec.Name)
	require.Contains(t, spec.Description, "Allowed projects: OPS. Allowed issue types: Task, Bug. Allowed labels: incident, Prod. At most 3 issues a day.")
	require.JSONEq(t, `["Task", "Bug"]`, string(mustField(t, spec.ParamsSchema, "issueType", "enum")))
}

// mustField returns a field of a property of a JSON Schema.
func mustField(t *testing.T, schema json.RawMessage, property, field string) json.RawMessage {
	t.Helper()

	var s struct {
		Properties map[string]map[string]json.RawMessage `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(schema, &s))

	return s.Properties[property][field]
}
