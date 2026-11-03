//go:build integration

package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// propBig is a pull request proposal that adds one file of 500 lines.
const (
	propBig  = "0192d3a4-0000-7000-8000-0000000000fa"
	bigPath  = "rules/big.yaml"
	bigLines = 500
	cutLines = 400
)

// diffLineBody is a line of a diff as the API returns it.
type diffLineBody struct {
	Kind    string `json:"kind"`
	OldLine *int   `json:"oldLine"`
	NewLine *int   `json:"newLine"`
	Text    string `json:"text"`
}

// fileDiffBody is the diff of a file as the API returns it.
type fileDiffBody struct {
	Path         string         `json:"path"`
	Status       string         `json:"status"`
	Lines        []diffLineBody `json:"lines"`
	Truncated    bool           `json:"truncated"`
	FinalNewline string         `json:"finalNewline"`
}

// diffProblemBody is the problem details of a refused request for a diff.
type diffProblemBody struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// numberText writes a line number, or a dot for none.
func numberText(n *int) string {
	if n == nil {
		return "."
	}

	return fmt.Sprint(*n)
}

// renderDiff writes the lines as the tests read them: the sign, the old and
// the new number and the text.
func renderDiff(lines []diffLineBody) []string {
	sign := map[string]string{"context": " ", "added": "+", "removed": "-"}
	out := make([]string, 0, len(lines))

	for _, l := range lines {
		out = append(out, fmt.Sprintf("%s%s/%s %s", sign[l.Kind], numberText(l.OldLine), numberText(l.NewLine), l.Text))
	}

	return out
}

// diffRoute is the route of the diff of a file of a proposal.
func diffRoute(id, path string, full bool) string {
	route := actionProposalsRoute + "/" + id + "/files/diff?path=" + url.QueryEscape(path)
	if full {
		route += "&full=true"
	}

	return route
}

// setupFileDiff returns a server with the pull request proposals and an
// action that reads the base files of the proposals from blobs.
func setupFileDiff(t *testing.T, reader apiReaderAction) server {
	t.Helper()

	s := setupFiles(t, map[string]action.Action{"monitor-pr": reader, "monitor-note": apiNoteAction{}})

	var content strings.Builder
	for i := 1; i <= bigLines; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}

	params, err := json.Marshal(map[string]any{
		"repo": "acme/monitors", "baseBranch": "main", "title": "Add a big file", "body": "",
		"files": []map[string]any{{"path": bigPath, "content": content.String()}},
	})
	require.NoError(t, err)

	snapshot := fmt.Sprintf(`{"target": "github:acme/monitors", "before": {%q: null},
		"after": {%q: {"blob": "b1", "status": "added", "linesAdded": %d, "linesRemoved": 0}}, "version": "base1"}`, bigPath, bigPath, bigLines)

	_, err = s.pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
		                              proposed_at, expires_at, decided_by, decided_at, snapshot)
		VALUES ($1::uuid, '0192d3a4-0000-7000-8000-0000000000e2', 'monitor-pr', $2::jsonb, 'Add it.', 'approved', 'rejected',
		        'sidekick/' || $1::uuid::text, now() - interval '3 hours', now() + interval '21 hours',
		        '01920000-0000-7000-8000-000000000071', now() - interval '170 minutes', $3::jsonb)`, propBig, string(params), snapshot)
	require.NoError(t, err)

	return s
}

// baseBlobs are the base files of the rules of the pull request proposal.
var baseBlobs = map[string]string{
	"a1": "one\nTWO\nthree\nfour\n",
	"c1": "x\ny\n",
}

func readFileDiff(t *testing.T, rec *httptest.ResponseRecorder) fileDiffBody {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body fileDiffBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	return body
}

func TestGetProposalFileDiff(t *testing.T) {
	t.Parallel()

	s := setupFileDiff(t, apiReaderAction{blobs: baseBlobs})

	tests := []struct {
		name         string
		id           string
		path         string
		wantStatus   string
		want         []string
		finalNewline string
	}{
		{
			name: "a changed file", id: propPullRequest, path: "rules/a.yaml", wantStatus: "changed",
			want: []string{" 1/1 one", "-2/. TWO", "+./2 two", " 3/3 three", "-4/. four"},
		},
		{name: "a new file is read from nowhere", id: propPullRequest, path: "rules/b.yaml", wantStatus: "added", want: []string{"+./1 x", "+./2 y", "+./3 z"}},
		{name: "a deleted file", id: propPullRequest, path: "rules/c.yaml", wantStatus: "deleted", want: []string{"-1/. x", "-2/. y"}},
		{name: "a rejected proposal still answers", id: propBig, path: bigPath, wantStatus: "added"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := readFileDiff(t, s.doWithToken(t, http.MethodGet, diffRoute(tt.id, tt.path, false), approverToken, nil))
			require.Equal(t, tt.path, got.Path)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.finalNewline, got.FinalNewline)

			if tt.want != nil {
				require.Equal(t, tt.want, renderDiff(got.Lines))
				require.False(t, got.Truncated)
			}
		})
	}
}

func TestGetProposalFileDiffCutsAtFourHundredLines(t *testing.T) {
	t.Parallel()

	s := setupFileDiff(t, apiReaderAction{blobs: baseBlobs})

	cut := readFileDiff(t, s.doWithToken(t, http.MethodGet, diffRoute(propBig, bigPath, false), approverToken, nil))
	require.True(t, cut.Truncated)
	require.Len(t, cut.Lines, cutLines)
	require.Equal(t, "+./1 line 1", renderDiff(cut.Lines)[0])

	full := readFileDiff(t, s.doWithToken(t, http.MethodGet, diffRoute(propBig, bigPath, true), approverToken, nil))
	require.False(t, full.Truncated)
	require.Len(t, full.Lines, bigLines)
	require.Equal(t, "+./500 line 500", renderDiff(full.Lines)[bigLines-1])
}

func TestGetProposalFileDiffSpellsHiddenCharacters(t *testing.T) {
	t.Parallel()

	s := setupFileDiff(t, apiReaderAction{blobs: map[string]string{"a1": "ab\u202ecd\r\n", "c1": ""}})

	got := readFileDiff(t, s.doWithToken(t, http.MethodGet, diffRoute(propPullRequest, "rules/a.yaml", false), approverToken, nil))
	require.Equal(t, []string{"-1/. ab\\u202ecd\\u000d", "+./1 one", "+./2 two", "+./3 three"}, renderDiff(got.Lines))
}

func TestGetProposalFileDiffRefusals(t *testing.T) {
	t.Parallel()

	s := setupFileDiff(t, apiReaderAction{blobs: map[string]string{"c1": "x\ny\n"}})
	noReader := setupFiles(t, map[string]action.Action{"monitor-pr": apiNoteAction{}, "monitor-note": apiNoteAction{}})
	failing := setupFileDiff(t, apiReaderAction{err: fmt.Errorf("blob %s is not a UTF-8 text file", "a1")})

	tests := []struct {
		name       string
		server     server
		token      string
		path       string
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{name: "a viewer", server: s, token: "skt_viewer", path: diffRoute(propPullRequest, "rules/c.yaml", false), wantStatus: http.StatusForbidden, wantDetail: "This needs the approver role."},
		{name: "an empty path", server: s, token: approverToken, path: diffRoute(propPullRequest, "", false), wantStatus: http.StatusBadRequest, wantDetail: "path is required."},
		{name: "a proposal that does not exist", server: s, token: approverToken, path: diffRoute(propNotThere, "rules/a.yaml", false), wantStatus: http.StatusNotFound, wantDetail: "No action proposal has this ID."},
		{name: "a path that is not in the snapshot", server: s, token: approverToken, path: diffRoute(propPullRequest, "rules/other.yaml", false), wantStatus: http.StatusNotFound, wantDetail: "This proposal does not change a file with this path."},
		{name: "a proposal that changes no files", server: s, token: approverToken, path: diffRoute(propSucceeded, "rules/a.yaml", false), wantStatus: http.StatusNotFound, wantDetail: "This proposal does not change a file with this path."},
		{
			name: "an action that cannot read files", server: noReader, token: approverToken, path: diffRoute(propPullRequest, "rules/a.yaml", false),
			wantStatus: http.StatusConflict, wantCode: "diff_unavailable", wantDetail: "The action that made this proposal cannot read files from its host.",
		},
		{name: "a blob the host does not have", server: s, token: approverToken, path: diffRoute(propPullRequest, "rules/a.yaml", false), wantStatus: http.StatusBadGateway, wantDetail: "Sidekick could not read the file on the base branch from the host: the host has no such blob"},
		{name: "a blob that is not text", server: failing, token: approverToken, path: diffRoute(propPullRequest, "rules/a.yaml", false), wantStatus: http.StatusBadGateway, wantDetail: "Sidekick could not read the file on the base branch from the host: blob a1 is not a UTF-8 text file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := tt.server.doWithToken(t, http.MethodGet, tt.path, tt.token, nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			var got diffProblemBody
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, tt.wantCode, got.Code)
			require.Equal(t, tt.wantDetail, got.Detail)
		})
	}
}

func TestGetProposalFileDiffNeedsAPath(t *testing.T) {
	t.Parallel()

	s := setupFileDiff(t, apiReaderAction{blobs: baseBlobs})

	rec := s.doWithToken(t, http.MethodGet, actionProposalsRoute+"/"+propPullRequest+"/files/diff", approverToken, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "path")
}
