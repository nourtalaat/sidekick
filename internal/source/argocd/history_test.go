package argocd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// historyApp returns the answer for shop/checkout with n history entries,
// stored oldest first as Argo CD stores them: entry i has the ID i and the
// revision rev<i>. The newest is started by the application controller, the
// next one by ada, and the one after that by nobody the server knows.
func historyApp(n int, spec string) fakehttp.Response {
	entries := make([]string, 0, n)

	for i := range n {
		initiated := ""

		switch i {
		case n - 1:
			initiated = `,"initiatedBy":{"automated":true}`
		case n - 2:
			initiated = `,"initiatedBy":{"username":"ada"}`
		}

		entries = append(entries, fmt.Sprintf(`{"id":%d,"revision":"rev%02d","deployedAt":"2026-11-01T%02d:00:00Z","deployStartedAt":"2026-11-01T%02d:00:00Z",
			"source":{"repoURL":"https://git.example.com/shop/deploy.git","path":"apps/checkout","targetRevision":"main"}%s}`,
			i, i, i%24, i%24, initiated))
	}

	return appBody(spec, `"history":[`+strings.Join(entries, ",")+`]`)
}

// metadataPath returns the path of the revision metadata call of rev<i>.
func metadataPath(i int) string {
	return fmt.Sprintf("%s/revisions/rev%02d/metadata", checkoutPath, i)
}

// commitBody returns the answer of the revision metadata call for a commit
// that says "Change <i>".
func commitBody(i int) fakehttp.Response {
	return body(fmt.Sprintf(`{"author":"Ada <ada@example.com>","date":"2026-11-01T%02d:00:00Z","tags":["v1.%d"],"message":"Change %d"}`, i, i, i))
}

func TestSourceGetHistory(t *testing.T) {
	t.Parallel()

	const (
		entries = 12
		// failingID is the ID of the entry whose commit Argo CD cannot read.
		failingID = 7
	)

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, checkoutPath, historyApp(entries, `"syncPolicy":{"automated":{}},"revisionHistoryLimit":12`))

	// Only the newest maxCommits revisions are read; one of them cannot be.
	for i := entries - 1; i >= entries-maxCommits; i-- {
		if i == failingID {
			api.Handle(http.MethodGet, metadataPath(i), failed(http.StatusInternalServerError, "repository not reachable"))

			continue
		}

		api.Handle(http.MethodGet, metadataPath(i), commitBody(i))
	}

	api.ExpectAllCalled()

	s := newTestSource(t, api, tokenSecrets(t))

	res, err := s.Call(t.Context(), source.Call{Tool: toolGetHistory, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
	require.NoError(t, err)
	require.False(t, res.Truncated)
	require.False(t, res.NoData)

	var got historyResult
	require.NoError(t, json.Unmarshal(res.Structured, &got))

	require.Equal(t, "shop/checkout", got.Application)
	require.True(t, got.AutoSync)
	require.Equal(t, 12, got.RevisionHistoryLimit)
	require.Equal(t, api.URL()+"/applications/argocd/checkout", got.URL)
	require.Equal(t, 1, got.CommitsUnavailable)
	require.Len(t, got.Entries, entries)

	for i, e := range got.Entries {
		id := int64(entries - 1 - i)
		require.Equal(t, id, e.ID, "newest first")
		require.Equal(t, i == 0, e.Current, "only the newest entry is deployed now")
		require.Equal(t, fmt.Sprintf("rev%02d", id), e.Revision)
		require.Equal(t, &sourceView{RepoURL: "https://git.example.com/shop/deploy.git", Path: "apps/checkout", TargetRevision: "main"}, e.Source)
		require.False(t, e.DeployedAt.IsZero())

		switch {
		case i >= maxCommits:
			require.Nil(t, e.Commit, "only the newest %d entries get a commit", maxCommits)
		case id == failingID:
			require.Nil(t, e.Commit, "a commit Argo CD cannot read is left out")
		default:
			require.Equal(t, &commitView{
				Message: fmt.Sprintf("Change %d", id),
				Author:  "Ada <ada@example.com>",
				Date:    time.Date(2026, 11, 1, int(id), 0, 0, 0, time.UTC),
				Tags:    []string{fmt.Sprintf("v1.%d", id)},
			}, e.Commit)
		}
	}

	require.Equal(t, "automated", got.Entries[0].InitiatedBy)
	require.Equal(t, "user:ada", got.Entries[1].InitiatedBy)
	require.Empty(t, got.Entries[2].InitiatedBy)

	require.Equal(t, []source.Evidence{{Kind: source.EvidenceDeploy, Title: "Argo CD history of shop/checkout",
		URL: api.URL() + "/applications/argocd/checkout", At: time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)}}, res.Evidence)

	for _, req := range api.Requests()[1:] {
		require.Equal(t, "shop", req.Query.Get("project"))
		require.Equal(t, "argocd", req.Query.Get("appNamespace"))
		require.Equal(t, "Bearer "+testToken, req.Header.Get("Authorization"))
		require.NotEmpty(t, req.Query.Get("versionId"))
	}
}

func TestSourceGetHistoryLongerThanLimit(t *testing.T) {
	t.Parallel()

	const entries = maxHistory + 5

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, checkoutPath, historyApp(entries, `"revisionHistoryLimit":100`))

	for i := entries - 1; i >= entries-maxCommits; i-- {
		api.Handle(http.MethodGet, metadataPath(i), commitBody(i%24))
	}

	api.ExpectAllCalled()

	s := newTestSource(t, api, tokenSecrets(t))

	res, err := s.Call(t.Context(), source.Call{Tool: toolGetHistory, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
	require.NoError(t, err)
	require.True(t, res.Truncated)

	var got historyResult
	require.NoError(t, json.Unmarshal(res.Structured, &got))
	require.Len(t, got.Entries, maxHistory)
	require.Equal(t, int64(entries-1), got.Entries[0].ID)
	require.Equal(t, int64(entries-maxHistory), got.Entries[maxHistory-1].ID)
	require.Zero(t, got.CommitsUnavailable)
	require.Equal(t, 100, got.RevisionHistoryLimit)
	require.False(t, got.AutoSync)
}

func TestSourceGetHistoryShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response fakehttp.Response
		check    func(t *testing.T, got historyResult)
	}{
		{
			name: "an application with several sources has no commits",
			response: appBody(`"sources":[{"repoURL":"https://git.example.com/a.git"},{"repoURL":"https://git.example.com/b.git"}]`,
				`"history":[{"id":3,"revisions":["aaaa","bbbb"],"deployedAt":"2026-11-02T09:00:00Z",
					"sources":[{"repoURL":"https://git.example.com/a.git","targetRevision":"v1"},{"repoURL":"https://git.example.com/b.git","path":"values"}],
					"source":{"repoURL":""},"initiatedBy":{"username":"grace"}}]`),
			check: func(t *testing.T, got historyResult) {
				t.Helper()

				require.Len(t, got.Entries, 1)
				e := got.Entries[0]
				require.Equal(t, []string{"aaaa", "bbbb"}, e.Revisions)
				require.Empty(t, e.Revision)
				require.Nil(t, e.Source)
				require.Len(t, e.Sources, 2)
				require.Equal(t, "values", e.Sources[1].Path)
				require.Nil(t, e.Commit)
				require.Zero(t, got.CommitsUnavailable, "an entry with several sources is not counted as missing a commit")
				require.Equal(t, "user:grace", e.InitiatedBy)
			},
		},
		{
			name:     "an entry written by an old Argo CD has no source and nobody who started it",
			response: appBody(``, `"history":[{"id":0,"revision":"","deployedAt":"2026-11-02T09:00:00Z","source":{"repoURL":""}}]`),
			check: func(t *testing.T, got historyResult) {
				t.Helper()

				require.Len(t, got.Entries, 1)
				require.Nil(t, got.Entries[0].Source)
				require.Empty(t, got.Entries[0].InitiatedBy)
				require.Nil(t, got.Entries[0].Commit)
				require.Zero(t, got.CommitsUnavailable)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, checkoutPath, tt.response)
			api.ExpectAllCalled()

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: toolGetHistory, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
			require.NoError(t, err)

			var got historyResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))

			tt.check(t, got)
			require.Len(t, api.Requests(), 1, "no commit is read")
		})
	}
}

func TestSourceGetHistoryNoData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response fakehttp.Response
		wantText string
		wantLink bool
	}{
		{
			name:     "an application that never synced",
			response: appBody(``, ``),
			wantText: "Argo CD has no deployment history for shop/checkout: it never synced, or its revisionHistoryLimit is 0.",
			wantLink: true,
		},
		{
			name:     "an application Argo CD does not have",
			response: failed(http.StatusNotFound, `applications.argoproj.io "checkout" not found`),
			wantText: notFoundText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, checkoutPath, tt.response)

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: toolGetHistory, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
			require.NoError(t, err)
			require.True(t, res.NoData)
			require.Equal(t, tt.wantText, res.Text)
			require.Empty(t, res.Structured)

			if tt.wantLink {
				require.Equal(t, []source.Evidence{{Kind: source.EvidenceDeploy, Title: "Argo CD history of shop/checkout",
					URL: api.URL() + "/applications/argocd/checkout"}}, res.Evidence)
			}
		})
	}
}

func TestSourceGetHistoryRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "an application outside the globs", input: `{"application":"billing/invoices"}`, want: `application "billing/invoices" is not one Sidekick may read`},
		{name: "a bare name", input: `{"application":"checkout"}`, want: "must have the form project/name"},
		{name: "no application", input: `{}`, want: "must have the form project/name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, registry.NewMockSecrets(gomock.NewController(t)))

			_, err := s.Call(t.Context(), source.Call{Tool: toolGetHistory, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches Argo CD")
		})
	}
}

func TestSourceGetHistoryFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		response      fakehttp.Response
		wantToolError string
		wantErr       string
	}{
		{name: "a token that may not get the application", response: failed(http.StatusForbidden, "permission denied"), wantToolError: forbiddenText},
		{name: "a refused token", response: failed(http.StatusUnauthorized, "invalid session"), wantErr: "Argo CD answered 401: invalid session"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, checkoutPath, tt.response)

			s := newTestSource(t, api, tokenSecrets(t))

			_, err := s.Call(t.Context(), source.Call{Tool: toolGetHistory, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
			require.Error(t, err)

			toolErr, isToolErr := errors.AsType[*source.ToolError](err)

			if tt.wantToolError != "" {
				require.True(t, isToolErr)
				require.Equal(t, tt.wantToolError, toolErr.Message)

				return
			}

			require.False(t, isToolErr)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSourceGetHistoryContextEnds(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, checkoutPath, historyApp(2, ``))
	api.Handle(http.MethodGet, metadataPath(1), fakehttp.Response{Headers: jsonHeaders, Body: []byte(`{}`), Delay: time.Minute})
	api.Handle(http.MethodGet, metadataPath(0), fakehttp.Response{Headers: jsonHeaders, Body: []byte(`{}`), Delay: time.Minute})

	s := newTestSource(t, api, tokenSecrets(t))

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, err := s.Call(ctx, source.Call{Tool: toolGetHistory, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, isToolErr := errors.AsType[*source.ToolError](err)
	require.False(t, isToolErr)
}

func TestSourceGetHistoryHidesSecretParameters(t *testing.T) {
	t.Parallel()

	const source1 = `{"repoURL":"https://git.example.com/a.git","helm":{"parameters":[{"name":"db.password","value":"hunter2"},{"name":"image.tag","value":"v1"}]}}`

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, checkoutPath, appBody(``, `"history":[
		{"id":1,"revision":"aaaa","deployedAt":"2026-11-02T09:00:00Z","source":`+source1+`},
		{"id":2,"revisions":["bbbb","cccc"],"deployedAt":"2026-11-03T09:00:00Z","sources":[
			{"repoURL":"https://git.example.com/a.git","helm":{"parameters":[{"name":"redis.auth.token","value":"hunter3"}]}},
			{"repoURL":"https://git.example.com/b.git","helm":{"parameters":[{"name":"TLS_KEY","value":"hunter4"},{"name":"keycloak.url","value":"https://sso"}]}}]}]`))
	api.Handle(http.MethodGet, "/api/v1/applications/checkout/revisions/aaaa/metadata", body(`{}`))

	s := newTestSource(t, api, tokenSecrets(t))

	res, err := s.Call(t.Context(), source.Call{Tool: toolGetHistory, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
	require.NoError(t, err)

	for _, secret := range []string{"hunter2", "hunter3", "hunter4"} {
		require.NotContains(t, string(res.Structured), secret)
	}

	var got historyResult
	require.NoError(t, json.Unmarshal(res.Structured, &got))
	require.Len(t, got.Entries, 2)
	require.Equal(t, []string{"redis.auth.token=[hidden]"}, got.Entries[0].Sources[0].Helm.Parameters)
	require.Equal(t, []string{"TLS_KEY=[hidden]", "keycloak.url=https://sso"}, got.Entries[0].Sources[1].Helm.Parameters)
	require.Equal(t, []string{"db.password=[hidden]", "image.tag=v1"}, got.Entries[1].Source.Helm.Parameters)
}
