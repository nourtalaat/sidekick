package linear

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output/sinkhttp"
	"github.com/nourtalaat/sidekick/internal/output/ticket"
)

// testIssue is the issue in fixture/issue.json.
var testIssue = ticket.Issue{ID: testIssueID, Key: "OPS-123", Scope: "OPS"}

func TestKeyFromURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		url    string
		want   string
		wantOK bool
	}{
		{name: "an issue link", url: "https://linear.app/acme/issue/OPS-123/checkout-is-slow", want: "OPS-123", wantOK: true},
		{name: "without the title part", url: "https://linear.app/acme/issue/ops-7", want: "OPS-7", wantOK: true},
		{name: "the workspace in another case", url: "https://LINEAR.app/Acme/issue/OPS-7/", want: "OPS-7", wantOK: true},
		{name: "another workspace", url: "https://linear.app/other/issue/OPS-123/x"},
		{name: "a project link", url: "https://linear.app/acme/project/checkout-123"},
		{name: "another host", url: "https://linear.example.com/acme/issue/OPS-123"},
		{name: "plain http", url: "http://linear.app/acme/issue/OPS-123"},
		{name: "not an identifier", url: "https://linear.app/acme/issue/checkout/x"},
		{name: "not a URL", url: "OPS-123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := newClient(DefaultAPIURL, "acme", nil, nil).KeyFromURL(tt.url)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response fakehttp.Response
		want     ticket.Issue
		wantErr  error
	}{
		{name: "found", response: fakehttp.Response{BodyFile: "fixture/issue.json"}, want: testIssue},
		{name: "not found", response: fakehttp.Response{BodyFile: "fixture/issue_not_found.json"}, wantErr: ticket.ErrNotFound},
		{name: "rate limited", response: fakehttp.Response{Status: http.StatusBadRequest, BodyFile: "fixture/rate_limited.json"}, wantErr: sinkhttp.ErrRateLimited},
		{name: "a server error", response: fakehttp.Response{Status: http.StatusBadGateway, Body: []byte("<html>bad gateway</html>")}, wantErr: &sinkhttp.StatusError{Status: http.StatusBadGateway}},
		{name: "an answer that is not JSON", response: fakehttp.Response{Body: []byte("nope")}, wantErr: errAny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, pathGraphQL, tt.response)
			api.ExpectAllCalled()

			got, err := testClient(t, api).Issue(t.Context(), "OPS-123")
			requireErr(t, tt.wantErr, err)
			require.Equal(t, tt.want, got)

			req := requests(t, api)[0]
			require.Contains(t, req.Query, "issue(id: $id)")
			require.Equal(t, map[string]any{"id": "OPS-123"}, req.Variables)
		})
	}
}

func TestCreateComment(t *testing.T) {
	t.Parallel()

	comment := ticket.Comment{IdempotencyKey: "sidekick:linear-comment:inv:0", Doc: ticket.Doc{Blocks: []ticket.Block{{Kind: ticket.Paragraph, Lines: []ticket.Line{{{Text: "Real issue"}}}}}}}
	derivedID := commentID(comment.IdempotencyKey)

	tests := []struct {
		name      string
		responses []fakehttp.Response
		want      string
		wantErr   error
		wantCalls []string
	}{
		{
			name:      "created",
			responses: []fakehttp.Response{{BodyFile: "fixture/comment_created.json"}},
			want:      testCommentID,
			wantCalls: []string{"commentCreate"},
		},
		{
			name:      "an earlier attempt went through, so the comment is edited",
			responses: []fakehttp.Response{{BodyFile: "fixture/comment_create_refused.json"}, {BodyFile: "fixture/comment_updated.json"}},
			want:      derivedID,
			wantCalls: []string{"commentCreate", "commentUpdate"},
		},
		{
			name:      "refused, and there is no comment to edit",
			responses: []fakehttp.Response{{BodyFile: "fixture/comment_create_refused.json"}, {BodyFile: "fixture/comment_not_found.json"}},
			wantErr:   &apiError{Code: "INVALID_INPUT"},
			wantCalls: []string{"commentCreate", "commentUpdate"},
		},
		{
			name:      "a server error is not taken for an existing comment",
			responses: []fakehttp.Response{{Status: http.StatusServiceUnavailable}},
			wantErr:   &sinkhttp.StatusError{Status: http.StatusServiceUnavailable},
			wantCalls: []string{"commentCreate"},
		},
		{
			name:      "a rate limit is not taken for an existing comment",
			responses: []fakehttp.Response{{Status: http.StatusBadRequest, BodyFile: "fixture/rate_limited.json"}},
			wantErr:   sinkhttp.ErrRateLimited,
			wantCalls: []string{"commentCreate"},
		},
		{
			name:      "an answer without success",
			responses: []fakehttp.Response{{Body: []byte(`{"data":{"commentCreate":{"success":false}}}`)}},
			wantErr:   errUnsuccessful,
			wantCalls: []string{"commentCreate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, pathGraphQL, tt.responses...)
			api.ExpectAllCalled()

			got, err := testClient(t, api).CreateComment(t.Context(), testIssue, comment)
			requireErr(t, tt.wantErr, err)
			require.Equal(t, tt.want, got)

			reqs := requests(t, api)
			require.Len(t, reqs, len(tt.wantCalls))

			for i, call := range tt.wantCalls {
				require.Contains(t, reqs[i].Query, call+"(")
			}

			input, ok := reqs[0].Variables["input"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, map[string]any{"id": derivedID, "issueId": testIssueID, "body": "Real issue"}, input)

			if len(reqs) > 1 {
				require.Equal(t, derivedID, reqs[1].Variables["id"])
			}
		})
	}
}

func TestUpdateComment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response fakehttp.Response
		wantErr  error
	}{
		{name: "updated", response: fakehttp.Response{BodyFile: "fixture/comment_updated.json"}},
		{name: "deleted", response: fakehttp.Response{BodyFile: "fixture/comment_not_found.json"}, wantErr: ticket.ErrNotFound},
		{name: "a server error", response: fakehttp.Response{Status: http.StatusInternalServerError}, wantErr: &sinkhttp.StatusError{Status: http.StatusInternalServerError}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, pathGraphQL, tt.response)
			api.ExpectAllCalled()

			err := testClient(t, api).UpdateComment(t.Context(), testIssue, testCommentID, ticket.Comment{})
			requireErr(t, tt.wantErr, err)

			req := requests(t, api)[0]
			require.Equal(t, testCommentID, req.Variables["id"])
		})
	}
}

func TestErrorsHoldNoMessageText(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathGraphQL, fakehttp.Response{Body: []byte(`{"errors":[{"message":"body has <secret text>","extensions":{"code":"not a code!"}}]}`)})

	_, err := testClient(t, api).Issue(t.Context(), "OPS-1")
	require.EqualError(t, err, "look up issue OPS-1: Linear answered OTHER")
}

func TestCommentID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{name: "first comment", key: "sidekick:linear-comment:inv:0"},
		{name: "after a deleted one", key: "sidekick:linear-comment:inv:1"},
	}

	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id := commentID(tt.key)
			require.Regexp(t, v4, id)
			require.Equal(t, id, commentID(tt.key))
		})
	}

	require.NotEqual(t, commentID(tests[0].key), commentID(tests[1].key))
}

// errAny stands for an error of no particular kind.
var errAny = errors.New("any error")

// requireErr checks err against want: nil, errAny for any error, a
// *sinkhttp.StatusError or *apiError compared by value, or a sentinel.
func requireErr(t *testing.T, want, err error) {
	t.Helper()

	if want == nil {
		require.NoError(t, err)

		return
	}

	if errors.Is(want, errAny) {
		require.Error(t, err)

		return
	}

	if w, ok := errors.AsType[*apiError](want); ok {
		got, ok := errors.AsType[*apiError](err)
		require.True(t, ok, "want %v, got %v", want, err)
		require.Equal(t, w.Code, got.Code)

		return
	}

	if w, ok := errors.AsType[*sinkhttp.StatusError](want); ok {
		got, ok := errors.AsType[*sinkhttp.StatusError](err)
		require.True(t, ok, "want %v, got %v", want, err)
		require.Equal(t, w.Status, got.Status)

		return
	}

	require.ErrorIs(t, err, want)
}

// testClient returns a client for the fake server.
func testClient(t *testing.T, api *fakehttp.Server) *client {
	t.Helper()

	hc, err := sinkhttp.New(Type, api.Client(), prometheus.NewRegistry())
	require.NoError(t, err)

	return newClient(api.URL()+pathGraphQL, "acme", hc, func(context.Context) (string, error) { return testAPIKey, nil })
}
