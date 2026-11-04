package githubpr

import (
	"encoding/json"
	"fmt"
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

func TestRefusalsMatchErrLimit(t *testing.T) {
	t.Parallel()

	const baseFile = `{"type":%q,"name":"rules.yaml","path":%q,"sha":"s","size":%d,"encoding":"base64","content":""}`

	tests := []struct {
		name string
		// serve makes the fake GitHub answer for testRulesPath.
		serve func(b *bed)
		// change edits the parameters.
		change func(p *Params)
		// wantAtSnapshot is false for a refusal that only CheckProposal makes.
		wantAtSnapshot bool
		wantErr        string
	}{
		{
			name: "a symlink",
			serve: func(b *bed) {
				b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fmt.Sprintf(baseFile, "symlink", testRulesPath, 5)))
			},
			wantAtSnapshot: true,
			wantErr:        "is not a regular file",
		},
		{
			name: "a submodule",
			serve: func(b *bed) {
				b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fmt.Sprintf(baseFile, "submodule", testRulesPath, 0)))
			},
			wantAtSnapshot: true,
			wantErr:        "is not a regular file",
		},
		{
			name: "a directory",
			serve: func(b *bed) {
				b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply("["+fmt.Sprintf(baseFile, "file", testRulesPath, 0)+"]"))
			},
			wantAtSnapshot: true,
			wantErr:        "is not a regular file",
		},
		{
			name: "a file that is too big",
			serve: func(b *bed) {
				b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fmt.Sprintf(baseFile, "file", testRulesPath, maxBaseFileBytes+1)))
			},
			wantAtSnapshot: true,
			wantErr:        "is larger than",
		},
		{
			name:           "a file that is not text",
			serve:          func(b *bed) { b.expectFile(testRulesPath, "a\x00b") },
			wantAtSnapshot: true,
			wantErr:        "is not a UTF-8 text file",
		},
		{
			name:    "a file that has the new content already",
			serve:   func(b *bed) { b.expectFile(testRulesPath, testRulesAfter) },
			wantErr: "already has this content",
		},
		{
			name:    "a delete of a file that is not there",
			serve:   func(b *bed) { b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), notFound) },
			change:  func(p *Params) { p.Files[0] = gitchange.FileChange{Path: testRulesPath, Delete: true} },
			wantErr: "cannot be deleted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" at CheckProposal", func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.expectBase()
			tt.serve(b)

			p := rulesParams()
			if tt.change != nil {
				tt.change(&p)
			}

			err := b.pr.CheckProposal(t.Context(), invocation(p).Params)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorIs(t, err, action.ErrPermanent)
		})

		if !tt.wantAtSnapshot {
			continue
		}

		t.Run(tt.name+" at Snapshot", func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.expectBase()
			tt.serve(b)

			_, err := b.pr.Snapshot(t.Context(), invocation(rulesParams()).Params)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorIs(t, err, action.ErrPermanent)
		})
	}
}

func TestValidateRefusalsMatchErrLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(p *Params)
		wantErr string
	}{
		{name: "no files", change: func(p *Params) { p.Files = nil }, wantErr: "give at least one file"},
		{name: "a path that is not clean", change: func(p *Params) { p.Files[0].Path = "prometheus/../rules.yaml" }, wantErr: "not a clean path"},
		{name: "content and delete", change: func(p *Params) { p.Files[0].Delete = true }, wantErr: "give either content or delete"},
		{name: "content that is not text", change: func(p *Params) { p.Files[0].Content = text("a\x00b") }, wantErr: "valid UTF-8"},
		{name: "no title", change: func(p *Params) { p.Title = " " }, wantErr: "title: required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := rulesParams()
			tt.change(&p)

			err := newBed(t, nil).pr.Validate(t.Context(), invocation(p).Params)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
		})
	}
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
