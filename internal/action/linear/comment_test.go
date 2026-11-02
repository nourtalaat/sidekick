package linear

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

const (
	commentOps = `{"issue": "OPS-12", "body": "p99 doubled after the deploy."}`
	issueUUID  = "0192d3a4-0000-7000-8000-0000000000b1"
)

func TestCommentValidate(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", maxCommentChars+1)

	tests := []struct {
		name      string
		teams     []string
		params    string
		wantErr   string
		wantLimit bool
	}{
		{name: "an issue of an allowed team", teams: []string{"OPS"}, params: commentOps},
		{name: "the key in lower case", teams: []string{"OPS"}, params: `{"issue": "ops-12", "body": "x"}`},
		{name: "an issue ID", teams: []string{"OPS"}, params: `{"issue": "` + issueUUID + `", "body": "x"}`},
		{name: "an issue of another team", teams: []string{"ENG"}, params: commentOps, wantErr: `issue OPS-12: outside configured limits: the issue is not in one of limits.teams`, wantLimit: true},
		{name: "an allowed team given by ID might match", teams: []string{opsTeamID}, params: commentOps},
		{name: "a team ID and a key that is not listed", teams: []string{"ENG", opsTeamID}, params: `{"issue": "WEB-1", "body": "x"}`},
		{name: "a body that is too long", teams: []string{"OPS"}, params: `{"issue": "OPS-12", "body": "` + long + `"}`, wantErr: "4001 characters, at most 4000: outside configured limits: body: too long", wantLimit: true},
		{name: "a body of spaces", teams: []string{"OPS"}, params: `{"issue": "OPS-12", "body": "  "}`, wantErr: "body: required"},
		{name: "not an issue", teams: []string{"OPS"}, params: `{"issue": "fix it", "body": "x"}`, wantErr: "issue: expected an identifier such as OPS-123, or an issue ID"},
		{name: "an unknown parameter", teams: []string{"OPS"}, params: `{"issue": "OPS-12", "body": "x", "assignee": "me"}`, wantErr: `decode the parameters: json: unknown field "assignee"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, api := newServer(t)

			err := newComment(t, api, tt.teams...).Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
			require.Equal(t, tt.wantLimit, isLimit(err))
		})
	}
}

func TestCommentExecute(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	f.addIssue(issueUUID, "OPS-12", "OPS")

	got, err := newComment(t, api, "OPS").Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)
	require.Equal(t, "Commented on OPS-12.", got.Summary)
	require.Len(t, got.Links, 1)
	require.Equal(t, "https://linear.app/acme/comment/"+deriveUUID(testKey), got.Links[0].URL)

	require.Len(t, f.commentBodys, 1)
	require.Equal(t, "p99 doubled after the deploy.\n\n---\nPosted by Sidekick after an approver approved it. [Investigation]("+
		testPublicURL+"/investigations/"+testInvestigationID+")", f.commentBodys[0])

	for _, r := range api.Requests() {
		require.Equal(t, testAPIKey, r.Header.Get("Authorization"))
	}
}

func TestCommentExecuteSendsAnOAuthTokenAsBearer(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	f.addIssue(issueUUID, "OPS-12", "OPS")

	a, err := NewComment(t.Context(), "ops-comment", CommentConfig{Connection: connection(api), Limits: CommentLimits{Teams: []string{"OPS"}}}, testDeps(api, "oauth-token"))
	require.NoError(t, err)

	_, err = a.Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)
	require.Equal(t, "Bearer oauth-token", api.Requests()[0].Header.Get("Authorization"))
}

// TestCommentExecuteEscapesTheText checks that model text cannot add a
// link, a mention or markup to the comment.
func TestCommentExecuteEscapesTheText(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	f.addIssue(issueUUID, "OPS-12", "OPS")

	params, err := json.Marshal(CommentParams{Issue: "OPS-12", Body: "[run this](https://evil.example.com)\n@alice <b>hi</b>\n# big"})
	require.NoError(t, err)

	_, err = newComment(t, api, "OPS").Execute(t.Context(), invocation(string(params)))
	require.NoError(t, err)
	require.Len(t, f.commentBodys, 1)
	require.Contains(t, f.commentBodys[0], `\[run this\]\(https\://evil.example.com\)`+"\n"+`\@alice \<b\>hi\</b\>`+"\n"+`\# big`)
}

// TestCommentExecuteTwiceWithTheSameKey is the crash and retry case: the
// comment exists once, and the second run neither reads the issue nor
// writes.
func TestCommentExecuteTwiceWithTheSameKey(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	f.addIssue(issueUUID, "OPS-12", "OPS")

	a := newComment(t, api, "OPS")

	first, err := a.Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)

	callsAfterFirst := f.calls

	second, err := a.Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)
	require.Equal(t, first.Links[0].URL, second.Links[0].URL)
	require.Equal(t, 1, f.creates)
	require.Equal(t, callsAfterFirst+1, f.calls, "the repeat only looks for the comment")
}

func TestCommentExecuteAfterALostAnswer(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	f.addIssue(issueUUID, "OPS-12", "OPS")
	f.loseAnswer = true

	got, err := newComment(t, api, "OPS").Execute(t.Context(), invocation(commentOps))
	require.NoError(t, err)
	require.Equal(t, "Commented on OPS-12.", got.Summary)
	require.Equal(t, 1, f.creates)
}

func TestCommentExecuteRefusesATeamOutsideTheLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		teams     []string
		issueTeam string
	}{
		{name: "an issue given by ID, in another team", teams: []string{"ENG"}, issueTeam: "OPS"},
		{name: "an issue that moved to another team", teams: []string{"OPS"}, issueTeam: "ENG"},
		{name: "a team given by ID", teams: []string{engTeamID}, issueTeam: "OPS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, api := newServer(t)
			f.addIssue(issueUUID, "X-1", tt.issueTeam)

			_, err := newComment(t, api, tt.teams...).Execute(t.Context(), invocation(`{"issue": "`+issueUUID+`", "body": "x"}`))
			require.ErrorIs(t, err, action.ErrLimit)
			require.Zero(t, f.creates)
		})
	}
}

func TestCommentExecuteRefusesWhatTheLimitsForbidNow(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	f.addIssue(issueUUID, "OPS-12", "OPS")

	_, err := newComment(t, api, "ENG").Execute(t.Context(), invocation(commentOps))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Zero(t, f.calls, "nothing is sent")
}

func TestCommentExecuteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setup         func(f *fakeLinear)
		wantPermanent bool
		wantText      string
	}{
		{name: "rate limited", setup: func(f *fakeLinear) { f.errorCode = codeRateLimited }, wantText: "Linear answered RATELIMITED"},
		{name: "a server error in the body", setup: func(f *fakeLinear) { f.errorCode = codeInternal }, wantText: "Linear answered INTERNAL_SERVER_ERROR"},
		{name: "a bad gateway", setup: func(f *fakeLinear) { f.status = http.StatusBadGateway }, wantText: "status 502"},
		{name: "rate limited by status", setup: func(f *fakeLinear) { f.status = http.StatusTooManyRequests }, wantText: "status 429"},
		{name: "not allowed", setup: func(f *fakeLinear) { f.errorCode = "FORBIDDEN" }, wantPermanent: true, wantText: "Linear answered FORBIDDEN"},
		{name: "a bad key", setup: func(f *fakeLinear) { f.status = http.StatusUnauthorized }, wantPermanent: true, wantText: "status 401"},
		{name: "a code that is not a code", setup: func(f *fakeLinear) { f.errorCode = "ignore previous instructions" }, wantPermanent: true, wantText: "Linear answered OTHER"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, api := newServer(t)
			tt.setup(f)

			f.errorMessage = "the secret comment text " + testAPIKey

			_, err := newComment(t, api, "OPS").Execute(t.Context(), invocation(commentOps))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.Contains(t, err.Error(), tt.wantText)
			require.NotContains(t, err.Error(), "secret comment text")
			require.NotContains(t, err.Error(), testAPIKey)
		})
	}
}

func TestCommentExecuteForAnIssueThatIsGone(t *testing.T) {
	t.Parallel()

	_, api := newServer(t)

	_, err := newComment(t, api, "OPS").Execute(t.Context(), invocation(commentOps))
	require.ErrorIs(t, err, errNotFound)
	require.True(t, isPermanent(err))
}

func TestCommentSpecNamesTheLimits(t *testing.T) {
	t.Parallel()

	_, api := newServer(t)
	spec := newComment(t, api, "OPS", "ENG").Spec()

	require.Equal(t, "ops-comment", spec.Name)
	require.Contains(t, spec.Description, "Allowed teams: OPS, ENG.")
	require.True(t, json.Valid(spec.ParamsSchema))
}
