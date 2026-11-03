package githubpr

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

func TestBlobSHA(t *testing.T) {
	t.Parallel()

	require.Equal(t, "ce013625030ba8dba906f756967f9e9ca394464a", blobSHA("hello\n"), "what git hash-object gives")
	require.Equal(t, "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391", blobSHA(""), "the empty blob")
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.expectBase()
	b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, testRulesBefore, "sha-rules")))
	b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/new.yaml"), notFound)
	b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/old.yaml"), reply(fileBodyWithSHA("prometheus/rules/old.yaml", "a\nb\n", "sha-old")))
	b.github.ExpectAllCalled()

	params := validParams()
	params.Files = []gitchange.FileChange{
		{Path: testRulesPath, Content: text(testRulesAfter)},
		{Path: "prometheus/rules/new.yaml", Content: text("groups: []\nx: 1\n")},
		{Path: "prometheus/rules/old.yaml", Delete: true},
	}

	snap, err := b.pr.Snapshot(t.Context(), invocation(params).Params)
	require.NoError(t, err)

	require.Equal(t, "github:acme/monitors", snap.Target)
	require.Equal(t, testBaseSHA, snap.Version)
	require.JSONEq(t, `{
		"`+testRulesPath+`": {"blob": "sha-rules"},
		"prometheus/rules/new.yaml": null,
		"prometheus/rules/old.yaml": {"blob": "sha-old"}
	}`, string(snap.Before))
	require.JSONEq(t, `{
		"`+testRulesPath+`": {"blob": "`+blobSHA(testRulesAfter)+`", "status": "changed", "linesAdded": 1, "linesRemoved": 1},
		"prometheus/rules/new.yaml": {"blob": "`+blobSHA("groups: []\nx: 1\n")+`", "status": "added", "linesAdded": 2, "linesRemoved": 0},
		"prometheus/rules/old.yaml": {"status": "deleted", "linesRemoved": 2}
	}`, string(snap.After))

	changes, err := action.Changes(snap)
	require.NoError(t, err)
	require.LessOrEqual(t, len(changes), action.MaxChanges)
	require.False(t, snap.Deletes(), "the snapshot never says the target is removed")
}

func TestSnapshotOfALargestChangeFitsTheDifferenceLimit(t *testing.T) {
	t.Parallel()

	before := map[string]any{}
	after := map[string]any{}

	for i := range gitchange.MaxFilesCeiling {
		path := "prometheus/rules/" + string(rune('a'+i)) + ".yaml"
		added, removed := 1, 1
		before[path] = gitchange.BlobRef{Blob: "old"}
		after[path] = gitchange.AfterEntry{Blob: "new", Status: gitchange.FileChanged, LinesAdded: &added, LinesRemoved: &removed}
	}

	beforeRaw, err := json.Marshal(before)
	require.NoError(t, err)

	afterRaw, err := json.Marshal(after)
	require.NoError(t, err)

	changes, err := action.Changes(action.Snapshot{Target: "github:acme/monitors", Before: beforeRaw, After: afterRaw})
	require.NoError(t, err, "the most files the config allows stay within action.MaxChanges")
	require.Len(t, changes, action.MaxChanges)
}

func TestExecuteComparesWithTheSnapshot(t *testing.T) {
	t.Parallel()

	afterBlob := blobSHA(testRulesAfter)

	snapshot := func(before string) *action.Snapshot {
		return &action.Snapshot{
			Target:  "github:acme/monitors",
			Version: testBaseSHA,
			Before:  json.RawMessage(`{"` + testRulesPath + `": {"blob": "` + before + `"}}`),
			After:   json.RawMessage(`{"` + testRulesPath + `": {"blob": "` + afterBlob + `", "status": "changed", "linesAdded": 1, "linesRemoved": 1}}`),
		}
	}

	tests := []struct {
		name       string
		live       string
		liveSHA    string
		snap       *action.Snapshot
		wantPR     bool
		wantErrIs  error
		wantErr    string
		wantResult string
	}{
		{name: "the file is as it was when the model proposed", live: testRulesBefore, liveSHA: "sha-before", snap: snapshot("sha-before"), wantPR: true},
		{name: "no snapshot to compare with", live: testRulesBefore, liveSHA: "anything", wantPR: true},
		{
			name: "someone changed the file", live: "groups: []\n", liveSHA: "sha-edited", snap: snapshot("sha-before"),
			wantErrIs: action.ErrConflict, wantErr: `"` + testRulesPath + `" changed on the base branch after the proposal was made`,
		},
		{
			name: "the file is at the new content already", live: testRulesAfter, liveSHA: afterBlob, snap: snapshot("sha-before"),
			wantResult: "The change is on the base branch already, so no pull request was opened.",
		},
		{
			name: "a snapshot of other content", live: testRulesBefore, liveSHA: "sha-before",
			snap: &action.Snapshot{
				Before: json.RawMessage(`{"` + testRulesPath + `": {"blob": "sha-before"}}`),
				After:  json.RawMessage(`{"` + testRulesPath + `": {"blob": "` + blobSHA("something else") + `"}}`),
			},
			wantErrIs: action.ErrPermanent, wantErr: "is not the content the snapshot describes",
		},
		{
			name: "a snapshot that lacks the file", live: testRulesBefore, liveSHA: "sha-before",
			snap:      &action.Snapshot{Before: json.RawMessage(`{}`), After: json.RawMessage(`{}`)},
			wantErrIs: action.ErrPermanent, wantErr: "has no entry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.expectNoBranch()
			b.expectBase()
			b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, tt.live, tt.liveSHA)))

			if tt.wantPR {
				b.validatorPasses(1)
				b.expectPush()
				b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(12, true)))
				b.github.ExpectAllCalled()
			}

			inv := invocation(rulesParams())
			inv.Snapshot = tt.snap

			res, err := b.pr.Execute(t.Context(), inv)

			switch {
			case tt.wantErrIs != nil:
				require.ErrorIs(t, err, tt.wantErrIs)
				require.ErrorContains(t, err, tt.wantErr)
			case tt.wantResult != "":
				require.NoError(t, err)
				require.Equal(t, tt.wantResult, res.Summary)
				require.Empty(t, res.Links)
			default:
				require.NoError(t, err)
				require.Contains(t, res.Summary, "pull request")
			}

			if !tt.wantPR {
				for _, call := range sent(b.github) {
					require.NotContains(t, call, "POST", "nothing is written")
				}

				require.Empty(t, b.validator.Requests())
			}
		})
	}
}
