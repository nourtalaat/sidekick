package gitlab

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Shared data of the commit tests.
const (
	abcSHA = "abc1234def5678abc1234def5678abc1234def56"
	botSHA = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"

	mergeRequestsPath = apiRoot + "/projects/acme/checkout/merge_requests"
	releasesPath      = apiRoot + "/projects/acme/checkout/releases"
	tagsPath          = apiRoot + "/projects/acme/checkout/repository/tags"

	pipelinesPath   = apiRoot + "/projects/acme/checkout/pipelines"
	deploymentsPath = apiRoot + "/projects/acme/checkout/deployments"

	filePath = apiRoot + "/projects/acme/checkout/repository/files/deploy/values.yaml"
	treePath = apiRoot + "/projects/acme/checkout/repository/tree"

	commitPath  = apiRoot + "/projects/acme/checkout/repository/commits/" + abcSHA
	comparePath = apiRoot + "/projects/acme/checkout/repository/compare"
)

func TestSourceCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tool     string
		input    string
		pageSize int
		routes   []route
		// wantQueries are the encoded queries of the requests, in order.
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
			input:    `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","path":"config/","limit":3}`,
			pageSize: 2,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				page("commits-page-1.json", 2), page("commits-page-2.json", 0),
			}}},
			wantQueries: []string{
				"page=1&path=config%2F&per_page=2&since=2026-05-04T09%3A00%3A00Z",
				"page=2&path=config%2F&per_page=2&since=2026-05-04T09%3A00%3A00Z",
			},
			want: `{"project":"acme/checkout","commits":[
				{"sha":"abc1234def5678abc1234def5678abc1234def56","message":"Lower DB pool size to 5 to save connections\n\nThe pool was too large for the new database.","author":"Ada Lovelace","date":"2026-05-04T10:05:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/abc1234def5678abc1234def5678abc1234def56"},
				{"sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c","message":"Bump the image tag","author":"Deploy Bot","date":"2026-05-04T09:30:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"},
				{"sha":"9988776655443322110099887766554433221100","message":"Add a retry to the payment client","author":"Grace Hopper","date":"2026-05-04T09:10:00Z"}
			]}`,
			wantEvidence: []source.Evidence{
				commitLink("abc1234d", abcSHA, at(10, 5)),
				commitLink("0f1e2d3c", botSHA, at(9, 30)),
			},
		},
		{
			name:     "list_commits stops at the limit and finds the project by service",
			tool:     toolListCommits,
			input:    `{"service":"checkout-api","since":"2026-05-04T09:00:00Z","ref":"feature/x","limit":2}`,
			pageSize: 2,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				page("commits-page-1.json", 2),
			}}},
			wantQueries: []string{"page=1&per_page=2&ref_name=feature%2Fx&since=2026-05-04T09%3A00%3A00Z"},
			want: `{"project":"acme/checkout","commits":[
				{"sha":"abc1234def5678abc1234def5678abc1234def56","message":"Lower DB pool size to 5 to save connections\n\nThe pool was too large for the new database.","author":"Ada Lovelace","date":"2026-05-04T10:05:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/abc1234def5678abc1234def5678abc1234def56"},
				{"sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c","message":"Bump the image tag","author":"Deploy Bot","date":"2026-05-04T09:30:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"}
			]}`,
			wantEvidence: []source.Evidence{
				commitLink("abc1234d", abcSHA, at(10, 5)),
				commitLink("0f1e2d3c", botSHA, at(9, 30)),
			},
			wantTruncated: true,
		},
		{
			name:        "list_commits sends times in UTC",
			tool:        toolListCommits,
			input:       `{"project":"acme/checkout","since":"2026-05-04T11:00:00+02:00","until":"2026-05-04T12:00:00Z"}`,
			routes:      []route{{http.MethodGet, commitsPath, []fakehttp.Response{body("[]")}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z&until=2026-05-04T12%3A00%3A00Z"},
			wantNoData:  true,
		},
		{
			name:  "a project GitLab does not have is no data",
			tool:  toolListCommits,
			input: `{"project":"acme/missing","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, apiRoot + "/projects/acme/missing/repository/commits", []fakehttp.Response{
				failed(http.StatusNotFound, "404 Project Not Found"),
			}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantNoData:  true,
			wantText:    notFoundText,
		},
		{
			name:  "a request GitLab refuses is a tool error with its message",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				failed(http.StatusForbidden, "403 Forbidden"),
			}}},
			wantQueries:   []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantToolError: "GitLab answered 403: 403 Forbidden",
		},
		{
			name:  "an error message that is an object is kept as JSON",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				{Status: http.StatusBadRequest, Headers: jsonHeaders, Body: []byte(`{"message":{"ref_name":["is invalid"]}}`)},
			}}},
			wantQueries:   []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantToolError: `GitLab answered 400: {"ref_name":["is invalid"]}`,
		},
		{
			name:  "a refused token is a failure of the source",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				failed(http.StatusUnauthorized, "401 Unauthorized"),
			}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantErr:     "GitLab answered 401: 401 Unauthorized",
		},
		{
			name:  "a rate limit is a failure of the source",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "30", "Content-Type": "text/plain"}, Body: []byte("Retry later")},
			}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantErr:     "GitLab answered 429",
		},
		{
			name:  "GitLab failing is a failure of the source",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				failed(http.StatusBadGateway, "502 Bad Gateway"),
			}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantErr:     "GitLab answered 502: 502 Bad Gateway",
		},
		{
			name:  "a second page that fails fails the call",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","limit":3}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				page("commits-page-1.json", 2), failed(http.StatusInternalServerError, "500 Internal Server Error"),
			}}},
			pageSize: 2,
			wantQueries: []string{
				"page=1&per_page=2&since=2026-05-04T09%3A00%3A00Z",
				"page=2&per_page=2&since=2026-05-04T09%3A00%3A00Z",
			},
			wantErr: "read page 2",
		},
		{
			name:     "get_commit reads the commit and pages through its diff",
			tool:     toolGetCommit,
			input:    `{"project":"acme/checkout","ref":"` + abcSHA + `"}`,
			pageSize: 2,
			routes: []route{
				{http.MethodGet, commitPath, []fakehttp.Response{fixture("commit.json")}},
				{http.MethodGet, commitPath + "/diff", []fakehttp.Response{page("commit-diff-page-1.json", 2), page("commit-diff-page-2.json", 0)}},
			},
			wantQueries: []string{"", "page=1&per_page=2", "page=2&per_page=2"},
			want: `{"project":"acme/checkout","sha":"abc1234def5678abc1234def5678abc1234def56",
				"message":"Lower DB pool size to 5 to save connections\n\nThe pool was too large for the new database.",
				"author":"Ada Lovelace","committer":"Ada Lovelace","date":"2026-05-04T10:05:00Z",
				"parents":["0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"],"additions":3,"deletions":1,
				"files":[
					{"filename":"config/db.yaml","status":"modified","additions":1,"deletions":1,"patch":"@@ -1,3 +1,3 @@\n pool:\n-  size: 20\n+  size: 5\n   timeout: 30\n"},
					{"filename":"docs/new.md","previousFilename":"docs/old.md","status":"renamed","additions":0,"deletions":0},
					{"filename":"scripts/new.sh","status":"added","additions":2,"deletions":0,"patch":"@@ -0,0 +1,2 @@\n+#!/bin/sh\n+echo hi\n"}
				],
				"url":"https://gitlab.example.com/acme/checkout/-/commit/abc1234def5678abc1234def5678abc1234def56"}`,
			wantEvidence: []source.Evidence{commitLink("abc1234d", abcSHA, at(10, 5))},
		},
		{
			name:  "get_commit sends a branch with a slash as one path part and finds nothing for a missing one",
			tool:  toolGetCommit,
			input: `{"project":"acme/checkout","ref":"feature/x"}`,
			routes: []route{{http.MethodGet, apiRoot + "/projects/acme/checkout/repository/commits/feature/x", []fakehttp.Response{
				failed(http.StatusNotFound, "404 Commit Not Found"),
			}}},
			wantQueries: []string{""},
			wantNoData:  true,
			wantText:    notFoundText,
		},
		{
			name:  "list_merge_requests keeps those opened or merged since the time and stops at the first one not touched since",
			tool:  toolListMergeRequests,
			input: `{"project":"acme/checkout","since":"2026-05-04T08:00:00Z","base":"main"}`,
			routes: []route{{http.MethodGet, mergeRequestsPath, []fakehttp.Response{
				fixture("merge-requests.json"),
			}}},
			wantQueries: []string{"order_by=updated_at&page=1&per_page=30&sort=desc&state=all&target_branch=main&updated_after=2026-05-04T08%3A00%3A00Z"},
			want: `{"project":"acme/checkout","mergeRequests":[
				{"iid":42,"title":"Draft: Add caching to the cart","state":"opened","draft":true,"author":"grace","base":"main","head":"cart-cache","created":"2026-05-04T09:30:00Z","url":"https://gitlab.example.com/acme/checkout/-/merge_requests/42"},
				{"iid":41,"title":"Lower DB pool size","state":"merged","author":"ada","base":"main","head":"db-pool","created":"2026-05-04T08:00:00Z","merged":"2026-05-04T09:50:00Z","mergeCommit":"abc1234def5678abc1234def5678abc1234def56","url":"https://gitlab.example.com/acme/checkout/-/merge_requests/41"}
			]}`,
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceCommit, Title: "acme/checkout!42", URL: "https://gitlab.example.com/acme/checkout/-/merge_requests/42", At: at(9, 30)},
				{Kind: source.EvidenceCommit, Title: "acme/checkout!41", URL: "https://gitlab.example.com/acme/checkout/-/merge_requests/41", At: at(9, 50)},
			},
		},
		{
			name:  "list_merge_requests stops at the limit",
			tool:  toolListMergeRequests,
			input: `{"project":"acme/checkout","since":"2026-05-04T08:00:00Z","limit":1}`,
			routes: []route{{http.MethodGet, mergeRequestsPath, []fakehttp.Response{
				fixture("merge-requests.json"),
			}}},
			wantQueries: []string{"order_by=updated_at&page=1&per_page=1&sort=desc&state=all&updated_after=2026-05-04T08%3A00%3A00Z"},
			want: `{"project":"acme/checkout","mergeRequests":[
				{"iid":42,"title":"Draft: Add caching to the cart","state":"opened","draft":true,"author":"grace","base":"main","head":"cart-cache","created":"2026-05-04T09:30:00Z","url":"https://gitlab.example.com/acme/checkout/-/merge_requests/42"}
			]}`,
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceCommit, Title: "acme/checkout!42", URL: "https://gitlab.example.com/acme/checkout/-/merge_requests/42", At: at(9, 30)},
			},
			wantTruncated: true,
		},
		{
			name:  "list_merge_requests with none is no data",
			tool:  toolListMergeRequests,
			input: `{"project":"acme/checkout","since":"2026-05-04T08:00:00Z"}`,
			routes: []route{{http.MethodGet, mergeRequestsPath, []fakehttp.Response{
				body("[]"),
			}}},
			wantQueries: []string{"order_by=updated_at&page=1&per_page=30&sort=desc&state=all&updated_after=2026-05-04T08%3A00%3A00Z"},
			wantNoData:  true,
		},
		{
			name:  "list_releases ends at the first release made before the time",
			tool:  toolListReleases,
			input: `{"project":"acme/checkout","since":"2026-05-03T00:00:00Z"}`,
			routes: []route{{http.MethodGet, releasesPath, []fakehttp.Response{
				fixture("releases.json"),
			}}},
			wantQueries: []string{"page=1&per_page=30"},
			want: `{"project":"acme/checkout","releases":[
				{"tag":"v1.4.0","name":"Checkout 1.4.0","commit":"abc1234def5678abc1234def5678abc1234def56","author":"ada","created":"2026-05-04T10:00:00Z","released":"2026-05-04T10:00:00Z","url":"https://gitlab.example.com/acme/checkout/-/releases/v1.4.0"}
			]}`,
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceDeploy, Title: "acme/checkout release v1.4.0", URL: "https://gitlab.example.com/acme/checkout/-/releases/v1.4.0", At: at(10, 0)},
			},
		},
		{
			name:  "list_releases adds the newest tags",
			tool:  toolListReleases,
			input: `{"project":"acme/checkout","includeTags":true}`,
			routes: []route{
				{http.MethodGet, releasesPath, []fakehttp.Response{fixture("releases.json")}},
				{http.MethodGet, tagsPath, []fakehttp.Response{fixture("tags.json")}},
			},
			wantQueries: []string{"page=1&per_page=30", "per_page=30"},
			want: `{"project":"acme/checkout","releases":[
				{"tag":"v1.4.0","name":"Checkout 1.4.0","commit":"abc1234def5678abc1234def5678abc1234def56","author":"ada","created":"2026-05-04T10:00:00Z","released":"2026-05-04T10:00:00Z","url":"https://gitlab.example.com/acme/checkout/-/releases/v1.4.0"},
				{"tag":"v1.3.2","name":"Checkout 1.3.2","commit":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c","author":"grace","created":"2026-05-02T09:00:00Z","released":"2026-05-02T10:00:00Z","url":"https://gitlab.example.com/acme/checkout/-/releases/v1.3.2"}
			],"tags":[
				{"name":"v1.4.0","sha":"abc1234def5678abc1234def5678abc1234def56","created":"2026-05-04T10:00:00Z"},
				{"name":"v1.3.2","sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"}
			]}`,
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceDeploy, Title: "acme/checkout release v1.4.0", URL: "https://gitlab.example.com/acme/checkout/-/releases/v1.4.0", At: at(10, 0)},
				{
					Kind: source.EvidenceDeploy, Title: "acme/checkout release v1.3.2", URL: "https://gitlab.example.com/acme/checkout/-/releases/v1.3.2",
					At: time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC),
				},
			},
		},
		{
			name:  "list_releases with no releases and no tags is no data",
			tool:  toolListReleases,
			input: `{"project":"acme/checkout","includeTags":true}`,
			routes: []route{
				{http.MethodGet, releasesPath, []fakehttp.Response{body("[]")}},
				{http.MethodGet, tagsPath, []fakehttp.Response{body("[]")}},
			},
			wantQueries: []string{"page=1&per_page=30", "per_page=30"},
			wantNoData:  true,
		},
		{
			name:  "list_pipelines ends at the first pipeline created before the time",
			tool:  toolListPipelines,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","ref":"main","status":"success","source":"web"}`,
			routes: []route{{http.MethodGet, pipelinesPath, []fakehttp.Response{
				fixture("pipelines.json"),
			}}},
			wantQueries: []string{"created_after=2026-05-04T09%3A00%3A00Z&order_by=id&page=1&per_page=30&ref=main&sort=desc&source=web&status=success"},
			want: `{"project":"acme/checkout","pipelines":[
				{"id":1002,"name":"Build pipeline","status":"running","source":"push","ref":"main","sha":"abc1234def5678abc1234def5678abc1234def56","created":"2026-05-04T10:06:00Z","updated":"2026-05-04T10:08:00Z","url":"https://gitlab.example.com/acme/checkout/-/pipelines/1002"},
				{"id":1001,"name":"Deploy pipeline","status":"success","source":"web","ref":"main","sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c","created":"2026-05-04T09:31:00Z","updated":"2026-05-04T09:40:00Z","url":"https://gitlab.example.com/acme/checkout/-/pipelines/1001"}
			]}`,
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceDeploy, Title: "acme/checkout pipeline #1002", URL: "https://gitlab.example.com/acme/checkout/-/pipelines/1002", At: at(10, 6)},
				{Kind: source.EvidenceDeploy, Title: "acme/checkout pipeline #1001", URL: "https://gitlab.example.com/acme/checkout/-/pipelines/1001", At: at(9, 31)},
			},
		},
		{
			name:  "list_pipelines without a time lists the newest pipelines",
			tool:  toolListPipelines,
			input: `{"project":"acme/checkout","limit":1}`,
			routes: []route{{http.MethodGet, pipelinesPath, []fakehttp.Response{
				fixture("pipelines.json"),
			}}},
			wantQueries: []string{"order_by=id&page=1&per_page=1&sort=desc"},
			want: `{"project":"acme/checkout","pipelines":[
				{"id":1002,"name":"Build pipeline","status":"running","source":"push","ref":"main","sha":"abc1234def5678abc1234def5678abc1234def56","created":"2026-05-04T10:06:00Z","updated":"2026-05-04T10:08:00Z","url":"https://gitlab.example.com/acme/checkout/-/pipelines/1002"}
			]}`,
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceDeploy, Title: "acme/checkout pipeline #1002", URL: "https://gitlab.example.com/acme/checkout/-/pipelines/1002", At: at(10, 6)},
			},
			wantTruncated: true,
		},
		{
			name:  "list_deployments keeps those created since the time and links to their page",
			tool:  toolListDeployments,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","environment":"production"}`,
			routes: []route{{http.MethodGet, deploymentsPath, []fakehttp.Response{
				fixture("deployments.json"),
			}}},
			wantQueries: []string{"environment=production&order_by=created_at&page=1&per_page=30&sort=desc&updated_after=2026-05-04T09%3A00%3A00Z"},
			want: `{"project":"acme/checkout","deployments":[
				{"id":41,"iid":7,"environment":"production","status":"success","ref":"main","sha":"abc1234def5678abc1234def5678abc1234def56","deployer":"deploy-bot","created":"2026-05-04T10:10:00Z","updated":"2026-05-04T10:12:00Z","finished":"2026-05-04T10:12:00Z","url":"{base}/acme/checkout/-/environments/9/deployments/7"},
				{"id":40,"iid":6,"environment":"production","status":"failed","ref":"main","sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c","deployer":"ada","created":"2026-05-04T09:30:00Z","updated":"2026-05-04T09:35:00Z","url":"{base}/acme/checkout/-/environments/9/deployments/6"}
			]}`,
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceDeploy, Title: "acme/checkout deploy production #7", URL: "{base}/acme/checkout/-/environments/9/deployments/7", At: at(10, 10)},
				{Kind: source.EvidenceDeploy, Title: "acme/checkout deploy production #6", URL: "{base}/acme/checkout/-/environments/9/deployments/6", At: at(9, 30)},
			},
		},
		{
			name:  "list_deployments with a status and no deployments is no data",
			tool:  toolListDeployments,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","status":"blocked"}`,
			routes: []route{{http.MethodGet, deploymentsPath, []fakehttp.Response{
				body("[]"),
			}}},
			wantQueries: []string{"order_by=created_at&page=1&per_page=30&sort=desc&status=blocked&updated_after=2026-05-04T09%3A00%3A00Z"},
			wantNoData:  true,
		},
		{
			name:  "get_file reads a file at a ref and links to the commit it read",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"/deploy/values.yaml/","ref":"main"}`,
			routes: []route{{http.MethodGet, filePath, []fakehttp.Response{
				fixture("file.json"),
			}}},
			wantQueries: []string{"ref=main"},
			want: `{"project":"acme/checkout","path":"deploy/values.yaml","ref":"main","type":"file",
				"sha":"79f7bbd25901e8334750839545a9bd021f0e4c83","size":30,"content":"replicas: 3\nimage: shop:1.4.0\n",
				"url":"{base}/acme/checkout/-/blob/d5a3ff139356ce33e37e73add446f16869741b50/deploy/values.yaml"}`,
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceResource,
				Title: "acme/checkout:deploy/values.yaml@main",
				URL:   "{base}/acme/checkout/-/blob/d5a3ff139356ce33e37e73add446f16869741b50/deploy/values.yaml",
			}},
		},
		{
			name:  "get_file without a ref reads the default branch",
			tool:  toolGetFile,
			input: `{"service":"checkout-api","path":"deploy/values.yaml"}`,
			routes: []route{{http.MethodGet, filePath, []fakehttp.Response{
				fixture("file.json"),
			}}},
			wantQueries: []string{"ref=HEAD"},
			want: `{"project":"acme/checkout","path":"deploy/values.yaml","type":"file",
				"sha":"79f7bbd25901e8334750839545a9bd021f0e4c83","size":30,"content":"replicas: 3\nimage: shop:1.4.0\n",
				"url":"{base}/acme/checkout/-/blob/d5a3ff139356ce33e37e73add446f16869741b50/deploy/values.yaml"}`,
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceResource,
				Title: "acme/checkout:deploy/values.yaml",
				URL:   "{base}/acme/checkout/-/blob/d5a3ff139356ce33e37e73add446f16869741b50/deploy/values.yaml",
			}},
		},
		{
			name:  "get_file lists a directory when the path is not a file",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"deploy","ref":"main"}`,
			routes: []route{
				{http.MethodGet, apiRoot + "/projects/acme/checkout/repository/files/deploy", []fakehttp.Response{failed(http.StatusNotFound, "404 File Not Found")}},
				{http.MethodGet, treePath, []fakehttp.Response{fixture("directory.json")}},
			},
			wantQueries: []string{"ref=main", "page=1&path=deploy&per_page=100&ref=main"},
			want: `{"project":"acme/checkout","path":"deploy","ref":"main","type":"dir","entries":[
				{"name":"templates","type":"dir"},{"name":"values.yaml","type":"file"},{"name":"vendor","type":"submodule"}]}`,
		},
		{
			name:  "get_file lists the root of the project for an empty path",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"/"}`,
			routes: []route{
				{http.MethodGet, treePath, []fakehttp.Response{fixture("directory.json")}},
			},
			wantQueries: []string{"page=1&per_page=100"},
			want: `{"project":"acme/checkout","path":"","type":"dir","entries":[
				{"name":"templates","type":"dir"},{"name":"values.yaml","type":"file"},{"name":"vendor","type":"submodule"}]}`,
		},
		{
			name:  "get_file finds nothing for a path that is neither a file nor a directory",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"deploy/values.yaml","ref":"main"}`,
			routes: []route{
				{http.MethodGet, filePath, []fakehttp.Response{failed(http.StatusNotFound, "404 File Not Found")}},
				{http.MethodGet, treePath, []fakehttp.Response{failed(http.StatusNotFound, "404 Tree Not Found")}},
			},
			wantQueries: []string{"ref=main", "page=1&path=deploy%2Fvalues.yaml&per_page=100&ref=main"},
			wantNoData:  true,
			wantText:    notFoundText,
		},
		{
			name:  "get_file finds nothing in an empty directory",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"deploy/values.yaml"}`,
			routes: []route{
				{http.MethodGet, filePath, []fakehttp.Response{failed(http.StatusNotFound, "404 File Not Found")}},
				{http.MethodGet, treePath, []fakehttp.Response{body("[]")}},
			},
			wantQueries: []string{"ref=HEAD", "page=1&path=deploy%2Fvalues.yaml&per_page=100"},
			wantNoData:  true,
		},
		{
			name:  "get_file does not show a binary file",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"deploy/values.yaml","ref":"main"}`,
			routes: []route{{http.MethodGet, filePath, []fakehttp.Response{
				body(`{"file_path":"deploy/values.yaml","size":3,"encoding":"base64","content":"AAEC","blob_id":"b1","commit_id":"c1"}`),
			}}},
			wantQueries: []string{"ref=main"},
			want:        `{"project":"acme/checkout","path":"deploy/values.yaml","ref":"main","type":"file","sha":"b1","size":3,"omitted":"binary file","url":"{base}/acme/checkout/-/blob/c1/deploy/values.yaml"}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceResource, Title: "acme/checkout:deploy/values.yaml@main", URL: "{base}/acme/checkout/-/blob/c1/deploy/values.yaml",
			}},
		},
		{
			name:  "get_file leaves out a file over 1 MB",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"deploy/values.yaml","ref":"main"}`,
			routes: []route{{http.MethodGet, filePath, []fakehttp.Response{
				body(`{"file_path":"deploy/values.yaml","size":2000000,"encoding":"base64","content":"","blob_id":"b2","commit_id":"c2"}`),
			}}},
			wantQueries: []string{"ref=main"},
			want:        `{"project":"acme/checkout","path":"deploy/values.yaml","ref":"main","type":"file","sha":"b2","size":2000000,"omitted":"file over 1 MB","url":"{base}/acme/checkout/-/blob/c2/deploy/values.yaml"}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceResource, Title: "acme/checkout:deploy/values.yaml@main", URL: "{base}/acme/checkout/-/blob/c2/deploy/values.yaml",
			}},
			wantTruncated: true,
		},
		{
			name:  "get_file passes on a ref GitLab refuses",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"deploy/values.yaml","ref":"nope"}`,
			routes: []route{{http.MethodGet, filePath, []fakehttp.Response{
				failed(http.StatusBadRequest, "400 Bad request - invalid ref"),
			}}},
			wantQueries:   []string{"ref=nope"},
			wantToolError: "GitLab answered 400: 400 Bad request - invalid ref",
		},
		{
			name:  "compare lists the newest commits first and the files, and says what GitLab left out",
			tool:  toolCompare,
			input: `{"project":"acme/checkout","base":"v1.3.2","head":"v1.4.0"}`,
			routes: []route{{http.MethodGet, comparePath, []fakehttp.Response{
				fixture("compare.json"),
			}}},
			wantQueries: []string{"from=v1.3.2&to=v1.4.0"},
			want: `{"project":"acme/checkout","base":"v1.3.2","head":"v1.4.0","totalCommits":2,
				"commits":[
					{"sha":"abc1234def5678abc1234def5678abc1234def56","message":"Lower DB pool size to 5 to save connections","author":"Ada Lovelace","date":"2026-05-04T10:05:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/abc1234def5678abc1234def5678abc1234def56"},
					{"sha":"9988776655443322110099887766554433221100","message":"Add a retry to the payment client","author":"Grace Hopper","date":"2026-05-04T09:10:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/9988776655443322110099887766554433221100"}
				],
				"files":[
					{"filename":"config/db.yaml","status":"modified","additions":1,"deletions":1},
					{"filename":"vendor/big.json","status":"modified","additions":0,"deletions":0,"omitted":"diff too large for GitLab to send"}
				],
				"url":"https://gitlab.example.com/acme/checkout/-/compare/v1.3.2...v1.4.0"}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceCommit, Title: "acme/checkout v1.3.2...v1.4.0", URL: "https://gitlab.example.com/acme/checkout/-/compare/v1.3.2...v1.4.0",
			}},
			wantTruncated: true,
		},
		{
			name:  "compare keeps only the newest commits up to the limit",
			tool:  toolCompare,
			input: `{"service":"checkout-api","base":"v1.3.2","head":"v1.4.0","limit":1}`,
			routes: []route{{http.MethodGet, comparePath, []fakehttp.Response{
				body(`{"commits":[
						{"id":"9988776655443322110099887766554433221100","title":"Add a retry to the payment client","author_name":"Grace Hopper","created_at":"2026-05-04T09:10:00Z"},
						{"id":"abc1234def5678abc1234def5678abc1234def56","title":"Lower DB pool size","author_name":"Ada Lovelace","created_at":"2026-05-04T10:05:00Z"}],
					"diffs":[],"compare_timeout":true,"web_url":"https://gitlab.example.com/acme/checkout/-/compare/v1.3.2...v1.4.0"}`),
			}}},
			wantQueries: []string{"from=v1.3.2&to=v1.4.0"},
			want: `{"project":"acme/checkout","base":"v1.3.2","head":"v1.4.0","totalCommits":2,
				"commits":[{"sha":"abc1234def5678abc1234def5678abc1234def56","message":"Lower DB pool size","author":"Ada Lovelace","date":"2026-05-04T10:05:00Z"}],
				"files":[],"timedOut":true,"url":"https://gitlab.example.com/acme/checkout/-/compare/v1.3.2...v1.4.0"}`,
			wantEvidence: []source.Evidence{{
				Kind: source.EvidenceCommit, Title: "acme/checkout v1.3.2...v1.4.0", URL: "https://gitlab.example.com/acme/checkout/-/compare/v1.3.2...v1.4.0",
			}},
			wantTruncated: true,
		},
		{
			name:  "compare of identical refs is no data",
			tool:  toolCompare,
			input: `{"project":"acme/checkout","base":"main","head":"main"}`,
			routes: []route{{http.MethodGet, comparePath, []fakehttp.Response{
				body(`{"commits":[],"diffs":[],"compare_timeout":false,"compare_same_ref":true,"web_url":""}`),
			}}},
			wantQueries: []string{"from=main&to=main"},
			wantNoData:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			var paths []string

			for _, r := range tt.routes {
				api.Handle(r.method, r.path, r.responses...)
				paths = append(paths, r.path)
			}

			api.ExpectAllCalled()

			s := newTestSource(t, api, tt.pageSize)

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

			wantEvidence := slices.Clone(tt.wantEvidence)
			for i := range wantEvidence {
				wantEvidence[i].URL = strings.ReplaceAll(wantEvidence[i].URL, "{base}", api.URL())
			}

			require.Equal(t, wantEvidence, got.Evidence)

			if tt.want == "" {
				require.Empty(t, got.Structured)

				return
			}

			require.JSONEq(t, strings.ReplaceAll(tt.want, "{base}", api.URL()), string(got.Structured))
		})
	}
}

func TestSourceCallInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{
			name:  "no project",
			tool:  toolListCommits,
			input: `{"since":"2026-05-04T09:00:00Z"}`,
			want:  "give project (the full path, such as group/project) or service",
		},
		{
			name:  "both project and service",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","service":"checkout-api"}`,
			want:  "give project or service, not both",
		},
		{
			name:  "an unknown service",
			tool:  toolListCommits,
			input: `{"service":"billing"}`,
			want:  `no project is configured for service "billing"; known services: checkout-api`,
		},
		{
			name:  "a project the config does not allow",
			tool:  toolListCommits,
			input: `{"project":"other/secret"}`,
			want:  `project "other/secret" is not one Sidekick may read; allowed: acme/**`,
		},
		{name: "a project path that climbs out", tool: toolListCommits, input: `{"project":"acme/.."}`, want: `project "acme/.." must be a full path such as group/project`},
		{name: "a numeric project ID", tool: toolListCommits, input: `{"project":"42"}`, want: `project "42" must be a full path such as group/project`},
		{
			name:  "an encoded project path",
			tool:  toolListCommits,
			input: `{"project":"acme%2Fcheckout"}`,
			want:  `project "acme%2Fcheckout" must be a full path such as group/project`,
		},
		{
			name:  "a project path with a query",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout?x=1"}`,
			want:  `project "acme/checkout?x=1" must be a full path such as group/project`,
		},
		{
			name:  "a time that does not parse",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"yesterday"}`,
			want:  `since: "yesterday" is not an RFC 3339 time such as 2026-05-04T10:00:00Z`,
		},
		{
			name:  "a ref with dots",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","ref":"main/../../x"}`,
			want:  `ref: "main/../../x" is not a valid branch, tag or commit SHA`,
		},
		{
			name:  "a ref with a query",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","ref":"main?x=1"}`,
			want:  `ref: "main?x=1" is not a valid branch, tag or commit SHA`,
		},
		{name: "no ref", tool: toolGetCommit, input: `{"project":"acme/checkout"}`, want: "give ref, the commit SHA"},
		{
			name:  "a path that climbs out",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"config/../../x"}`,
			want:  `path: "config/../../x" is not a path inside the project`,
		},
		{
			name:  "a ref with a query for get_file",
			tool:  toolGetFile,
			input: `{"project":"acme/checkout","path":"a","ref":"main?x=1"}`,
			want:  `ref: "main?x=1" is not a valid branch, tag or commit SHA`,
		},
		{
			name:  "a pipeline source with a slash",
			tool:  toolListPipelines,
			input: `{"project":"acme/checkout","source":"a/b"}`,
			want:  `source: "a/b" is not a pipeline source such as push or schedule`,
		},
		{
			name:  "an environment name with a control character",
			tool:  toolListDeployments,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","environment":"prod\u0000"}`,
			want:  "is not an environment name",
		},
		{name: "compare without head", tool: toolCompare, input: `{"project":"acme/checkout","base":"main"}`, want: "give both base and head"},
		{name: "compare with a ref that climbs out", tool: toolCompare, input: `{"project":"acme/checkout","base":"a/../b","head":"main"}`, want: `base: "a/../b" is not a valid branch, tag or commit SHA`},
		{name: "input that does not decode", tool: toolListCommits, input: `{"limit":"ten"}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, 0)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches GitLab")
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "create_merge_request"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t), 0)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr error
		wantMsg string
	}{
		{name: "gitlab.com by default", cfg: Config{}},
		{name: "a self-managed GitLab under a sub-path", cfg: Config{BaseURL: "https://example.com/gitlab/"}},
		{name: "no secrets resolver", cfg: Config{}, wantErr: errNoSecrets},
		{
			name:    "settings the guard would refuse",
			cfg:     Config{Settings: source.Settings{Tools: source.ToolSettings{Rename: map[string]string{toolListCommits: "bad name"}}}},
			wantMsg: "tools.rename.list_commits",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deps := registry.Deps{}
			if tt.wantErr == nil {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			s, err := New(t.Context(), "gitlab", tt.cfg, deps)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantMsg != "":
				require.ErrorContains(t, err, tt.wantMsg)
			default:
				require.NoError(t, err)

				_, ok := s.(*Source)
				require.True(t, ok)
			}
		})
	}
}
