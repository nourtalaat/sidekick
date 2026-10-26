package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestSourceCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tool     string
		input    string
		pageSize int
		routes   []route
		// exchanges is the number of token exchanges; zero means one.
		exchanges     int
		wantQueries   []string
		want          string
		wantEvidence  []source.Evidence
		wantTruncated bool
		wantNoData    bool
		wantText      string
		wantToolError string
		wantErr       string
	}{
		{
			name:     "list_commits pages through the commits",
			tool:     toolListCommits,
			input:    `{"repo":"acme/checkout","since":"2026-05-04T09:00:00Z","path":"config/","limit":3}`,
			pageSize: 2,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/commits", []fakehttp.Response{
				page("commits-page-1.json", 2), page("commits-page-2.json", 0),
			}}},
			wantQueries: []string{
				"path=config%2F&per_page=2&since=2026-05-04T09%3A00%3A00Z",
				"page=2&path=config%2F&per_page=2&since=2026-05-04T09%3A00%3A00Z",
			},
			want: `{"repo":"acme/checkout","commits":[
				{"sha":"abc1234def5678abc1234def5678abc1234def56","message":"Lower DB pool size to 5 to save connections\n\nThe pool was too large for the new database.","author":"ada","date":"2026-05-04T10:05:00Z","url":"https://github.com/acme/checkout/commit/abc1234def5678abc1234def5678abc1234def56"},
				{"sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c","message":"Bump the image tag","author":"Deploy Bot","date":"2026-05-04T09:30:00Z","url":"https://github.com/acme/checkout/commit/0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"},
				{"sha":"9988776655443322110099887766554433221100","message":"Add a retry to the payment client","author":"grace","date":"2026-05-04T09:10:00Z","url":"https://github.com/acme/checkout/commit/9988776655443322110099887766554433221100"}
			]}`,
			wantEvidence: []source.Evidence{
				commitLink("abc1234", "abc1234def5678abc1234def5678abc1234def56", at(10, 5)),
				commitLink("0f1e2d3", "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c", at(9, 30)),
				commitLink("9988776", "9988776655443322110099887766554433221100", at(9, 10)),
			},
		},
		{
			name:     "list_commits stops at the limit and finds the repository by service",
			tool:     toolListCommits,
			input:    `{"service":"checkout-api","since":"2026-05-04T09:00:00Z","ref":"main","limit":2}`,
			pageSize: 2,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/commits", []fakehttp.Response{
				page("commits-page-1.json", 2),
			}}},
			wantQueries: []string{"per_page=2&sha=main&since=2026-05-04T09%3A00%3A00Z"},
			want: `{"repo":"acme/checkout","commits":[
				{"sha":"abc1234def5678abc1234def5678abc1234def56","message":"Lower DB pool size to 5 to save connections\n\nThe pool was too large for the new database.","author":"ada","date":"2026-05-04T10:05:00Z","url":"https://github.com/acme/checkout/commit/abc1234def5678abc1234def5678abc1234def56"},
				{"sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c","message":"Bump the image tag","author":"Deploy Bot","date":"2026-05-04T09:30:00Z","url":"https://github.com/acme/checkout/commit/0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"}
			]}`,
			wantEvidence: []source.Evidence{
				commitLink("abc1234", "abc1234def5678abc1234def5678abc1234def56", at(10, 5)),
				commitLink("0f1e2d3", "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c", at(9, 30)),
			},
			wantTruncated: true,
		},
		{
			name:        "list_commits with no commits is no data",
			tool:        toolListCommits,
			input:       `{"repo":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes:      []route{{http.MethodGet, "/repos/acme/checkout/commits", []fakehttp.Response{body("[]")}}},
			wantQueries: []string{"per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantNoData:  true,
		},
		{
			name:  "a repository GitHub does not have is no data",
			tool:  toolListCommits,
			input: `{"repo":"acme/missing","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, "/repos/acme/missing/commits", []fakehttp.Response{
				{Status: http.StatusNotFound, Headers: jsonHeaders, Body: []byte(`{"message":"Not Found"}`)},
			}}},
			wantQueries: []string{"per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantNoData:  true,
			wantText:    notFoundText,
		},
		{
			name:  "an empty repository is no data",
			tool:  toolListCommits,
			input: `{"repo":"acme/new","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, "/repos/acme/new/commits", []fakehttp.Response{
				{Status: http.StatusConflict, Headers: jsonHeaders, Body: []byte(`{"message":"Git Repository is empty."}`)},
			}}},
			wantQueries: []string{"per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantNoData:  true,
			wantText:    emptyText,
		},
		{
			name:  "GitHub failing is a failure of the source",
			tool:  toolListCommits,
			input: `{"repo":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/commits", []fakehttp.Response{
				{Status: http.StatusBadGateway, Headers: jsonHeaders, Body: []byte(`{"message":"Server Error"}`)},
			}}},
			wantQueries: []string{"per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantErr:     "502 Server Error",
		},
		{
			name:  "a token GitHub refuses twice is a failure of the source",
			tool:  toolListCommits,
			input: `{"repo":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/commits", []fakehttp.Response{
				{Status: http.StatusUnauthorized, Headers: jsonHeaders, Body: []byte(`{"message":"Bad credentials"}`)},
				{Status: http.StatusUnauthorized, Headers: jsonHeaders, Body: []byte(`{"message":"Bad credentials"}`)},
			}}},
			exchanges: 2,
			wantQueries: []string{
				"per_page=30&since=2026-05-04T09%3A00%3A00Z",
				"per_page=30&since=2026-05-04T09%3A00%3A00Z",
			},
			wantErr: "401 Bad credentials",
		},
		{
			name:  "get_commit returns the files and patches",
			tool:  toolGetCommit,
			input: `{"repo":"acme/checkout","ref":"abc1234"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/commits/abc1234", []fakehttp.Response{
				fixture("commit.json"),
			}}},
			wantQueries: []string{""},
			want: `{"repo":"acme/checkout","sha":"abc1234def5678abc1234def5678abc1234def56",
				"message":"Lower DB pool size to 5 to save connections\n\nThe pool was too large for the new database.",
				"author":"ada","committer":"web-flow","date":"2026-05-04T10:05:00Z",
				"parents":["0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"],"additions":1,"deletions":1,
				"files":[
					{"filename":"config/db.yaml","status":"modified","additions":1,"deletions":1,"patch":"@@ -1,3 +1,3 @@\n database:\n-  poolSize: 20\n+  poolSize: 5\n"},
					{"filename":"docs/db.md","previousFilename":"docs/database.md","status":"renamed","additions":0,"deletions":0}
				],
				"url":"https://github.com/acme/checkout/commit/abc1234def5678abc1234def5678abc1234def56"}`,
			wantEvidence: []source.Evidence{commitLink("abc1234", "abc1234def5678abc1234def5678abc1234def56", at(10, 5))},
		},
		{
			name:  "get_commit for a ref GitHub cannot resolve is a tool error",
			tool:  toolGetCommit,
			input: `{"repo":"acme/checkout","ref":"nope"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/commits/nope", []fakehttp.Response{
				{Status: http.StatusUnprocessableEntity, Headers: jsonHeaders, Body: []byte(`{"message":"No commit found for SHA: nope"}`)},
			}}},
			wantQueries:   []string{""},
			wantToolError: "GitHub answered 422: No commit found for SHA: nope",
		},
		{
			name:  "list_pull_requests keeps those opened or merged since the time",
			tool:  toolListPullRequests,
			input: `{"repo":"acme/checkout","since":"2026-05-04T09:00:00Z","base":"main"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/pulls", []fakehttp.Response{
				page("pulls.json", 2),
			}}},
			wantQueries: []string{"base=main&direction=desc&per_page=100&sort=updated&state=all"},
			want: `{"repo":"acme/checkout","pullRequests":[
				{"number":42,"title":"Lower DB pool size","state":"merged","author":"ada","base":"main","head":"smaller-pool",
				 "created":"2026-05-03T16:00:00Z","merged":"2026-05-04T10:05:00Z","closed":"2026-05-04T10:05:00Z",
				 "mergeCommit":"abc1234def5678abc1234def5678abc1234def56","url":"https://github.com/acme/checkout/pull/42"},
				{"number":43,"title":"Add a dashboard link","state":"open","draft":true,"author":"grace","base":"main","head":"dashboard-link",
				 "created":"2026-05-04T09:45:00Z","url":"https://github.com/acme/checkout/pull/43"}
			]}`,
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceCommit, Title: "acme/checkout#42", URL: "https://github.com/acme/checkout/pull/42", At: at(10, 5)},
				{Kind: source.EvidenceCommit, Title: "acme/checkout#43", URL: "https://github.com/acme/checkout/pull/43", At: at(9, 45)},
			},
		},
		{
			name:  "list_releases stops at the first older release and lists tags",
			tool:  toolListReleases,
			input: `{"repo":"acme/checkout","since":"2026-05-01T00:00:00Z","includeTags":true,"limit":5}`,
			routes: []route{
				{http.MethodGet, "/repos/acme/checkout/releases", []fakehttp.Response{fixture("releases.json")}},
				{http.MethodGet, "/repos/acme/checkout/tags", []fakehttp.Response{fixture("tags.json")}},
			},
			wantQueries: []string{"per_page=5", "per_page=5"},
			want: `{"repo":"acme/checkout",
				"releases":[{"tag":"v1.4.0","name":"v1.4.0","target":"main","author":"deploy-bot",
					"created":"2026-05-04T10:06:00Z","published":"2026-05-04T10:07:00Z","url":"https://github.com/acme/checkout/releases/tag/v1.4.0"}],
				"tags":[{"name":"v1.4.0","sha":"abc1234def5678abc1234def5678abc1234def56"},{"name":"v1.3.2","sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"}]}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceDeploy, Title: "acme/checkout release v1.4.0",
				URL: "https://github.com/acme/checkout/releases/tag/v1.4.0", At: at(10, 7),
			}},
		},
		{
			name:  "list_workflow_runs of one workflow file",
			tool:  toolListWorkflowRuns,
			input: `{"repo":"acme/checkout","since":"2026-05-04T09:00:00Z","workflow":"deploy.yml","status":"success"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/actions/workflows/deploy.yml/runs", []fakehttp.Response{
				fixture("workflow-runs.json"),
			}}},
			wantQueries: []string{"created=%3E%3D2026-05-04T09%3A00%3A00Z&per_page=30&status=success"},
			want: `{"repo":"acme/checkout","workflowRuns":[{"id":30433642,"workflow":"Deploy",
				"title":"Lower DB pool size to 5 to save connections","number":562,"attempt":1,"event":"push",
				"status":"completed","conclusion":"success","branch":"main","sha":"abc1234def5678abc1234def5678abc1234def56",
				"actor":"ada","created":"2026-05-04T10:05:10Z","started":"2026-05-04T10:05:12Z","updated":"2026-05-04T10:09:40Z",
				"url":"https://github.com/acme/checkout/actions/runs/30433642"}]}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceDeploy, Title: "acme/checkout Deploy #562",
				URL: "https://github.com/acme/checkout/actions/runs/30433642", At: time.Date(2026, 5, 4, 10, 5, 10, 0, time.UTC),
			}},
		},
		{
			name:  "list_workflow_runs of a workflow ID with no runs",
			tool:  toolListWorkflowRuns,
			input: `{"repo":"acme/checkout","workflow":"1234"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/actions/workflows/1234/runs", []fakehttp.Response{
				body(`{"total_count":0,"workflow_runs":[]}`),
			}}},
			wantQueries: []string{"per_page=30"},
			wantNoData:  true,
		},
		{
			name:  "list_workflow_runs of every workflow",
			tool:  toolListWorkflowRuns,
			input: `{"repo":"acme/checkout","branch":"main","event":"push","limit":1}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/actions/runs", []fakehttp.Response{
				body(`{"total_count":0,"workflow_runs":[]}`),
			}}},
			wantQueries: []string{"branch=main&event=push&per_page=1"},
			wantNoData:  true,
		},
		{
			name:  "get_file reads a file",
			tool:  toolGetFile,
			input: `{"repo":"acme/checkout","path":"/config/db.yaml","ref":"main"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/contents/config/db.yaml", []fakehttp.Response{
				fixture("file.json"),
			}}},
			wantQueries: []string{"ref=main"},
			want: `{"repo":"acme/checkout","path":"config/db.yaml","ref":"main","type":"file",
				"sha":"3d21ec53a331a6f037a91c368710b99387d012c1","size":24,"content":"database:\n  poolSize: 5\n",
				"url":"https://github.com/acme/checkout/blob/main/config/db.yaml"}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceResource, Title: "acme/checkout:config/db.yaml@main",
				URL: "https://github.com/acme/checkout/blob/main/config/db.yaml",
			}},
		},
		{
			name:  "get_file lists a directory",
			tool:  toolGetFile,
			input: `{"repo":"acme/checkout","path":"config"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/contents/config", []fakehttp.Response{
				fixture("directory.json"),
			}}},
			wantQueries: []string{""},
			want: `{"repo":"acme/checkout","path":"config","type":"dir",
				"entries":[{"name":"db.yaml","type":"file","size":24},{"name":"env","type":"dir"}]}`,
		},
		{
			name:  "get_file leaves a binary file out",
			tool:  toolGetFile,
			input: `{"repo":"acme/checkout","path":"logo.png"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/contents/logo.png", []fakehttp.Response{
				body(`{"type":"file","encoding":"base64","size":9,"path":"logo.png","content":"AAECYmluYXJ5","html_url":"https://github.com/acme/checkout/blob/main/logo.png"}`),
			}}},
			wantQueries: []string{""},
			want: `{"repo":"acme/checkout","path":"logo.png","type":"file","size":9,"omitted":"binary file",
				"url":"https://github.com/acme/checkout/blob/main/logo.png"}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceResource, Title: "acme/checkout:logo.png", URL: "https://github.com/acme/checkout/blob/main/logo.png",
			}},
		},
		{
			name:  "get_file leaves a file over a megabyte out",
			tool:  toolGetFile,
			input: `{"repo":"acme/checkout","path":"data.csv"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/contents/data.csv", []fakehttp.Response{
				body(`{"type":"file","encoding":"none","size":2000000,"path":"data.csv","content":"","html_url":"https://github.com/acme/checkout/blob/main/data.csv"}`),
			}}},
			wantQueries: []string{""},
			want: `{"repo":"acme/checkout","path":"data.csv","type":"file","size":2000000,"omitted":"too large for the GitHub contents API",
				"url":"https://github.com/acme/checkout/blob/main/data.csv"}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceResource, Title: "acme/checkout:data.csv", URL: "https://github.com/acme/checkout/blob/main/data.csv",
			}},
			wantTruncated: true,
		},
		{
			name:  "compare keeps the newest commits",
			tool:  toolCompare,
			input: `{"repo":"acme/checkout","base":"v1.3.2","head":"v1.4.0","limit":1}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/compare/v1.3.2...v1.4.0", []fakehttp.Response{
				fixture("compare.json"),
			}}},
			wantQueries: []string{""},
			want: `{"repo":"acme/checkout","base":"v1.3.2","head":"v1.4.0","status":"ahead","aheadBy":2,"behindBy":0,"totalCommits":2,
				"commits":[{"sha":"abc1234def5678abc1234def5678abc1234def56","message":"Lower DB pool size to 5 to save connections","author":"ada",
					"date":"2026-05-04T10:05:00Z","url":"https://github.com/acme/checkout/commit/abc1234def5678abc1234def5678abc1234def56"}],
				"files":[{"filename":"config/db.yaml","status":"modified","additions":1,"deletions":1}],
				"url":"https://github.com/acme/checkout/compare/v1.3.2...v1.4.0"}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceCommit, Title: "acme/checkout v1.3.2...v1.4.0", URL: "https://github.com/acme/checkout/compare/v1.3.2...v1.4.0",
			}},
			wantTruncated: true,
		},
		{
			name:  "compare of identical refs is no data",
			tool:  toolCompare,
			input: `{"repo":"acme/checkout","base":"main","head":"main"}`,
			routes: []route{{http.MethodGet, "/repos/acme/checkout/compare/main...main", []fakehttp.Response{
				body(`{"status":"identical","ahead_by":0,"behind_by":0,"total_commits":0,"commits":[],"files":[]}`),
			}}},
			wantQueries: []string{""},
			wantNoData:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			exchanges := max(tt.exchanges, 1)

			for i := range exchanges {
				api.Handle(http.MethodPost, testExchangePath, tokenResponse("ghs_"+strconv.Itoa(i+1), time.Now().Add(time.Hour)))
			}

			var paths []string

			for _, r := range tt.routes {
				api.Handle(r.method, r.path, r.responses...)
				paths = append(paths, r.path)
			}

			api.ExpectAllCalled()

			s := newTestSource(t, api, exchanges, tt.pageSize)

			got, err := s.Call(t.Context(), source.Call{ID: "call-1", Tool: tt.tool, Input: json.RawMessage(tt.input)})
			require.Equal(t, tt.wantQueries, queries(api, paths))

			switch {
			case tt.wantToolError != "":
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Equal(t, tt.wantToolError, toolErr.Message)

				return
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)
				_, isToolErr := errors.AsType[*source.ToolError](err)
				require.False(t, isToolErr, "a failure of the source is not a tool error")

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantNoData, got.NoData)
			require.Equal(t, tt.wantText, got.Text)
			require.Equal(t, tt.wantTruncated, got.Truncated)
			require.Equal(t, tt.wantEvidence, got.Evidence)

			if tt.want == "" {
				require.Empty(t, got.Structured)

				return
			}

			require.JSONEq(t, tt.want, string(got.Structured))
		})
	}
}
