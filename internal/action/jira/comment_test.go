package jira

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

const commentOps = `{"issue": "OPS-12", "body": "p99 doubled after the deploy."}`

func TestCommentValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		projects  []string
		params    string
		wantErr   string
		wantLimit bool
	}{
		{name: "an issue of an allowed project", projects: []string{"OPS"}, params: commentOps},
		{name: "the key in lower case", projects: []string{"OPS"}, params: `{"issue": "ops-12", "body": "x"}`},
		{name: "an issue ID", projects: []string{"OPS"}, params: `{"issue": "10042", "body": "x"}`},
		{name: "an issue of another project", projects: []string{"ENG"}, params: commentOps, wantErr: "issue OPS-12: outside configured limits: the issue is not in one of limits.projects", wantLimit: true},
		{name: "a body that is too long", projects: []string{"OPS"}, params: `{"issue": "OPS-12", "body": "` + strings.Repeat("x", maxCommentChars+1) + `"}`, wantErr: "4001 characters, at most 4000: outside configured limits: body: too long", wantLimit: true},
		{name: "an empty body", projects: []string{"OPS"}, params: `{"issue": "OPS-12", "body": " "}`, wantErr: "body: required"},
		{name: "not an issue", projects: []string{"OPS"}, params: `{"issue": "fix it", "body": "x"}`, wantErr: "issue: expected a key such as OPS-123, or an issue ID"},
		{name: "an unknown parameter", projects: []string{"OPS"}, params: `{"issue": "OPS-12", "body": "x", "assignee": "me"}`, wantErr: `decode the parameters: json: unknown field "assignee"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, api := newServer(t)

			err := newComment(t, api, tt.projects...).Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
			require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
		})
	}
}

func TestCommentExecute(t *testing.T) {
	t.Parallel()

	f, api := newServer(t, [2]string{"OPS-12", "OPS"})

	got, err := newComment(t, api, "OPS").Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)
	require.Equal(t, "Commented on OPS-12.", got.Summary)
	require.Equal(t, []action.Link{{Title: "OPS-12 comment", URL: api.URL() + "/browse/OPS-12?focusedCommentId=500"}}, got.Links)

	issue := f.issues[0]
	require.Len(t, issue.Comments, 1)
	require.Equal(t, "p99 doubled after the deploy.|"+footerText+"| |Investigation|"+markerPrefix+keyHash(testKey), bodyText(t, issue.Comments[0].Body))

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testEmail+":"+testToken))
	for _, r := range api.Requests() {
		require.Equal(t, want, r.Header.Get("Authorization"))
	}
}

// TestCommentExecuteKeepsTheTextPlain checks that model text cannot add
// formatting, a link or a mention: it is only ever in text nodes, and the
// footer's link is the only mark in the document.
func TestCommentExecuteKeepsTheTextPlain(t *testing.T) {
	t.Parallel()

	const hostile = "[~accountid:abc] {code}x{code}\n*bold* [click|http://evil.example.com] !image.png!\n\n@admin"

	f, api := newServer(t, [2]string{"OPS-12", "OPS"})

	params, err := json.Marshal(CommentParams{Issue: "OPS-12", Body: hostile})
	require.NoError(t, err)

	_, err = newComment(t, api, "OPS").Execute(t.Context(), invocation(string(params)))
	require.NoError(t, err)

	var doc node
	require.NoError(t, json.Unmarshal(f.issues[0].Comments[0].Body, &doc))

	types := map[string]int{}
	marks := 0

	var walk func(n node)

	walk = func(n node) {
		types[n.Type]++
		marks += len(n.Marks)

		for _, c := range n.Content {
			walk(c)
		}
	}

	walk(doc)

	require.Equal(t, 1, marks, "only the link to the investigation")
	require.Zero(t, types["mention"])
	require.Zero(t, types["media"])
	require.Contains(t, bodyText(t, f.issues[0].Comments[0].Body), "[~accountid:abc] {code}x{code}|")
}

// TestCommentExecuteTwiceWithTheSameKey is the crash and retry case: one
// comment exists afterwards.
func TestCommentExecuteTwiceWithTheSameKey(t *testing.T) {
	t.Parallel()

	f, api := newServer(t, [2]string{"OPS-12", "OPS"})
	a := newComment(t, api, "OPS")

	first, err := a.Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)

	second, err := a.Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, f.creates)
}

func TestCommentExecuteAfterALostAnswer(t *testing.T) {
	t.Parallel()

	f, api := newServer(t, [2]string{"OPS-12", "OPS"})
	f.loseAnswer = true

	a := newComment(t, api, "OPS")

	_, err := a.Execute(t.Context(), invocation(commentOps))
	require.Error(t, err)
	require.False(t, isPermanent(err), "a bad gateway is tried again")

	got, err := a.Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)
	require.Equal(t, "Commented on OPS-12.", got.Summary)
	require.Equal(t, 1, f.creates)
}

func TestCommentExecuteIgnoresAnotherProposalsComment(t *testing.T) {
	t.Parallel()

	f, api := newServer(t, [2]string{"OPS-12", "OPS"})
	a := newComment(t, api, "OPS")

	other := invocation(commentOps)
	other.IdempotencyKey = "sidekick/another"

	_, err := a.Execute(t.Context(), other)
	require.NoError(t, err)

	_, err = a.Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)
	require.Equal(t, 2, f.creates)
}

func TestCommentExecuteRefusesAProjectOutsideTheLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		issueProject string
		projects     []string
		params       string
	}{
		{name: "an issue given by ID", issueProject: "OPS", projects: []string{"ENG"}, params: `{"issue": "10001", "body": "x"}`},
		{name: "an issue by key that moved projects", issueProject: "ENG", projects: []string{"OPS"}, params: `{"issue": "OPS-12", "body": "x"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, api := newServer(t, [2]string{"OPS-12", tt.issueProject})

			_, err := newComment(t, api, tt.projects...).Execute(t.Context(), invocation(tt.params))
			require.ErrorIs(t, err, action.ErrLimit)
			require.Zero(t, f.creates)
		})
	}
}

func TestCommentExecuteRefusesWhatTheLimitsForbidNow(t *testing.T) {
	t.Parallel()

	f, api := newServer(t, [2]string{"OPS-12", "OPS"})

	_, err := newComment(t, api, "ENG").Execute(t.Context(), invocation(commentOps))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Zero(t, f.calls, "nothing is sent")
}

func TestCommentExecuteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		wantPermanent bool
	}{
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "a bad gateway", status: http.StatusBadGateway},
		{name: "a timeout", status: http.StatusRequestTimeout},
		{name: "a bad token", status: http.StatusUnauthorized, wantPermanent: true},
		{name: "no rights", status: http.StatusForbidden, wantPermanent: true},
		{name: "no such issue", status: http.StatusNotFound, wantPermanent: true},
		{name: "a bad request", status: http.StatusBadRequest, wantPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, api := newServer(t, [2]string{"OPS-12", "OPS"})
			f.status = tt.status

			_, err := newComment(t, api, "OPS").Execute(t.Context(), invocation(commentOps))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.NotContains(t, err.Error(), testToken)
		})
	}
}

func TestCommentSpecNamesTheLimits(t *testing.T) {
	t.Parallel()

	_, api := newServer(t)
	spec := newComment(t, api, "OPS", "ENG").Spec()

	require.Equal(t, "ops-comment", spec.Name)
	require.Contains(t, spec.Description, "Allowed projects: OPS, ENG.")
	require.True(t, json.Valid(spec.ParamsSchema))
}
