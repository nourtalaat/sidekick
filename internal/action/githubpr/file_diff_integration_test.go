//go:build integration

package githubpr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// The proposals of the file diff tests, and the blobs of their base files.
const (
	diffProposal    = "0192d3a4-0000-7000-8000-0000000000a5"
	bigProposal     = "0192d3a4-0000-7000-8000-0000000000a6"
	changedBlob     = "1111111111111111111111111111111111111111"
	deletedBlob     = "2222222222222222222222222222222222222222"
	binaryBlob      = "3333333333333333333333333333333333333333"
	missingBlob     = "4444444444444444444444444444444444444444"
	newRulesPath    = "prometheus/rules/new.yaml"
	oldRulesPath    = "prometheus/rules/old.yaml"
	binaryPath      = "prometheus/rules/binary.yaml"
	missingPath     = "prometheus/rules/missing.yaml"
	bigPath         = "prometheus/rules/big.yaml"
	bigLines        = 500
	cutLines        = 400
	oldRulesContent = "old: 1\nold: 2\n"
)

// insertFileProposal stores a proposal of monitor-pr with the status, the
// parameters and the snapshot, as the service stored it before.
func insertFileProposal(t *testing.T, s *stack, id, status string, params any, snapshot string) {
	t.Helper()

	raw, err := json.Marshal(params)
	require.NoError(t, err)

	_, err = s.pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
		                              proposed_at, expires_at, snapshot)
		VALUES ($1::uuid, $2::uuid, 'monitor-pr', $3::jsonb, 'Tune it.', 'approved', $4, 'sidekick/' || $1::uuid::text,
		        now() - interval '2 days', now() - interval '1 day', $5::jsonb)`, id, checkoutInvestigation, string(raw), status, snapshot)
	require.NoError(t, err)
}

// afterEntryJSON is the entry of the after side of a snapshot.
func afterEntryJSON(status string, added, removed int) string {
	return fmt.Sprintf(`{"blob": "ffff", "status": %q, "linesAdded": %d, "linesRemoved": %d}`, status, added, removed)
}

func TestFileDiffReadsTheBaseFromTheHost(t *testing.T) {
	t.Parallel()

	s := newStack(t)

	params := validParams()
	params.Files = []gitchange.FileChange{
		{Path: testRulesPath, Content: text(testRulesAfter)},
		{Path: newRulesPath, Content: text("x: 1\ny: 2\n")},
		{Path: oldRulesPath, Delete: true},
		{Path: binaryPath, Content: text("z: 1\n")},
		{Path: missingPath, Content: text("z: 2\n")},
	}

	snapshot := fmt.Sprintf(`{
		"target": "github:acme/monitors",
		"before": {%q: {"blob": %q}, %q: null, %q: {"blob": %q}, %q: {"blob": %q}, %q: {"blob": %q}},
		"after": {%q: %s, %q: %s, %q: {"status": "deleted", "linesRemoved": 2}, %q: %s, %q: %s},
		"version": %q
	}`,
		testRulesPath, changedBlob, newRulesPath, oldRulesPath, deletedBlob, binaryPath, binaryBlob, missingPath, missingBlob,
		testRulesPath, afterEntryJSON("changed", 1, 1), newRulesPath, afterEntryJSON("added", 2, 0), oldRulesPath, binaryPath, afterEntryJSON("changed", 1, 1), missingPath, afterEntryJSON("changed", 1, 1),
		testBaseSHA)

	// An expired proposal still answers.
	insertFileProposal(t, s, diffProposal, "expired", params, snapshot)

	s.github.Handle(http.MethodGet, repoPath("git/blobs/"+changedBlob), fakehttp.Response{Body: []byte(testRulesBefore)})
	s.github.Handle(http.MethodGet, repoPath("git/blobs/"+deletedBlob), fakehttp.Response{Body: []byte(oldRulesContent)})
	s.github.Handle(http.MethodGet, repoPath("git/blobs/"+binaryBlob), fakehttp.Response{Body: []byte("a\x00b")})
	s.github.Handle(http.MethodGet, repoPath("git/blobs/"+missingBlob), notFound)
	s.github.ExpectAllCalled()

	id := uuid.MustParse(diffProposal)

	tests := []struct {
		name       string
		path       string
		want       []string
		wantStatus string
		wantErr    error
		unreadable bool
	}{
		{
			name: "a changed file", path: testRulesPath, wantStatus: "changed",
			want: []string{" 3/3   rules:", " 4/4   - alert: Slow", " 5/5     expr: rate(x[1m]) > 5", "-6/.     for: 1m", "+./6     for: 10m"},
		},
		{name: "a new file needs no read", path: newRulesPath, wantStatus: "added", want: []string{"+./1 x: 1", "+./2 y: 2"}},
		{name: "a deleted file", path: oldRulesPath, wantStatus: "deleted", want: []string{"-1/. old: 1", "-2/. old: 2"}},
		{name: "a binary file", path: binaryPath, unreadable: true, wantErr: errNotText},
		{name: "a blob the host does not have", path: missingPath, unreadable: true},
		{name: "a path that is not in the snapshot", path: "prometheus/rules/other.yaml", wantErr: proposals.ErrNoSuchFile},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := s.svc.FileDiff(t.Context(), id, tt.path, false)

			if tt.unreadable {
				var unreadable *proposals.UnreadableError

				require.ErrorAs(t, err, &unreadable)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.want, renderLines(got.Lines))
			require.False(t, got.Truncated)
		})
	}
}

func TestFileDiffCutsAtFourHundredLines(t *testing.T) {
	t.Parallel()

	s := newStack(t)

	var content strings.Builder
	for i := 1; i <= bigLines; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}

	params := validParams()
	params.Files = []gitchange.FileChange{{Path: bigPath, Content: text(content.String())}}

	snapshot := fmt.Sprintf(`{"target": "github:acme/monitors", "before": {%q: null}, "after": {%q: %s}, "version": %q}`,
		bigPath, bigPath, afterEntryJSON("added", bigLines, 0), testBaseSHA)

	// A rejected proposal still answers.
	insertFileProposal(t, s, bigProposal, "rejected", params, snapshot)

	id := uuid.MustParse(bigProposal)

	cut, err := s.svc.FileDiff(t.Context(), id, bigPath, false)
	require.NoError(t, err)
	require.True(t, cut.Truncated)
	require.Len(t, cut.Lines, cutLines)

	full, err := s.svc.FileDiff(t.Context(), id, bigPath, true)
	require.NoError(t, err)
	require.False(t, full.Truncated)
	require.Len(t, full.Lines, bigLines)
}

// renderLines writes lines as the tests read them: the sign, the old and the
// new number, and the text.
func renderLines(lines []gitchange.Line) []string {
	sign := map[string]string{gitchange.LineContext: " ", gitchange.LineAdded: "+", gitchange.LineRemoved: "-"}
	number := func(n int) string {
		if n == 0 {
			return "."
		}

		return fmt.Sprint(n)
	}

	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, fmt.Sprintf("%s%s/%s %s", sign[l.Kind], number(l.Old), number(l.New), l.Text))
	}

	return out
}
