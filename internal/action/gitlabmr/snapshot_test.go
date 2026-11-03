package gitlabmr

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
	b.gitlab.Handle(http.MethodGet, apiPath("repository/files/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, testRulesBefore, testRulesSHA)))
	b.expectNoFile("prometheus/rules/new.yaml")
	b.gitlab.Handle(http.MethodGet, apiPath("repository/files/prometheus/rules/old.yaml"), reply(fileBodyWithSHA("prometheus/rules/old.yaml", "a\nb\n", testOldSHA)))
	b.gitlab.ExpectAllCalled()

	params := validParams()
	params.Files = []gitchange.FileChange{
		{Path: testRulesPath, Content: text(testRulesAfter)},
		{Path: "prometheus/rules/new.yaml", Content: text("groups: []\nx: 1\n")},
		{Path: "prometheus/rules/old.yaml", Delete: true},
	}

	snap, err := b.mr.Snapshot(t.Context(), invocation(params).Params)
	require.NoError(t, err)

	require.Equal(t, "gitlab:"+testProject, snap.Target)
	require.Equal(t, testBaseSHA, snap.Version)
	require.JSONEq(t, `{
		"`+testRulesPath+`": {"blob": "`+testRulesSHA+`"},
		"prometheus/rules/new.yaml": null,
		"prometheus/rules/old.yaml": {"blob": "`+testOldSHA+`"}
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

	files := gitchange.FilesOf(snap)
	require.Len(t, files, 3, "the proposal lists the files as it does for a pull request")
}

func TestSnapshotRefusesWhatTheLimitsDoNotAllow(t *testing.T) {
	t.Parallel()

	t.Run("too many changed lines", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, func(l *Limits) { l.MaxChangedLines = 1 })
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)

		_, err := b.mr.Snapshot(t.Context(), invocation(rulesParams()).Params)
		require.ErrorIs(t, err, action.ErrLimit)
	})

	t.Run("a project outside the limits", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		p := rulesParams()
		p.Project = "acme/other"

		_, err := b.mr.Snapshot(t.Context(), invocation(p).Params)
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, b.gitlab.Requests())
	})
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

	changes, err := action.Changes(action.Snapshot{Target: "gitlab:" + testProject, Before: beforeRaw, After: afterRaw})
	require.NoError(t, err, "the most files the config allows stay within action.MaxChanges")
	require.Len(t, changes, action.MaxChanges)
}
