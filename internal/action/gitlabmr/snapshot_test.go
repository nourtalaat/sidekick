package gitlabmr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

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
		"`+testRulesPath+`": {"blob": "`+gitchange.BlobSHA(testRulesAfter)+`", "status": "changed", "linesAdded": 1, "linesRemoved": 1},
		"prometheus/rules/new.yaml": {"blob": "`+gitchange.BlobSHA("groups: []\nx: 1\n")+`", "status": "added", "linesAdded": 2, "linesRemoved": 0},
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

func TestRefusalsMatchErrLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// serve makes the fake GitLab answer for testRulesPath.
		serve func(b *bed)
		// change edits the parameters.
		change func(p *Params)
		// wantAtSnapshot is false for a refusal that only CheckProposal makes.
		wantAtSnapshot bool
		wantErr        string
	}{
		{
			name:           "a directory",
			serve:          func(b *bed) { b.expectNoFile(testRulesPath, treeEntry(testRulesPath, "tree", "040000")) },
			wantAtSnapshot: true,
			wantErr:        "is not a regular file",
		},
		{
			name: "a file that is too big",
			serve: func(b *bed) {
				b.gitlab.Handle(http.MethodGet, apiPath("repository/files/"+testRulesPath), reply(fileBody(testRulesPath, strings.Repeat("x", gitlabgit.MaxFileBytes+1))))
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
			name: "a file in a SHA-256 repository",
			serve: func(b *bed) {
				b.gitlab.Handle(http.MethodGet, apiPath("repository/files/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, testRulesBefore, strings.Repeat("a", 64))))
			},
			wantAtSnapshot: true,
			wantErr:        "SHA-256",
		},
		{
			name:    "a file that has the new content already",
			serve:   func(b *bed) { b.expectFile(testRulesPath, testRulesAfter) },
			wantErr: "already has this content",
		},
		{
			name:    "a delete of a file that is not there",
			serve:   func(b *bed) { b.expectNoFile(testRulesPath) },
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

			err := b.mr.CheckProposal(t.Context(), invocation(p).Params)
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

			_, err := b.mr.Snapshot(t.Context(), invocation(rulesParams()).Params)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorIs(t, err, action.ErrPermanent)
		})
	}
}

func TestReadError(t *testing.T) {
	t.Parallel()

	for _, sentinel := range fileRefusals {
		t.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			err := readError("read the file at the base", fmt.Errorf("a.yaml %w", sentinel))
			require.EqualError(t, err, "read the file at the base: a.yaml "+sentinel.Error())
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.ErrorIs(t, err, sentinel)
		})
	}

	t.Run("a call that failed", func(t *testing.T) {
		t.Parallel()

		err := readError("read the file at the base", errors.New("GitLab did not answer"))
		require.EqualError(t, err, "read the file at the base: GitLab did not answer")
		require.NotErrorIs(t, err, action.ErrLimit)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})
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

			err := newBed(t, nil).mr.Validate(t.Context(), invocation(p).Params)
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

	changes, err := action.Changes(action.Snapshot{Target: "gitlab:" + testProject, Before: beforeRaw, After: afterRaw})
	require.NoError(t, err, "the most files the config allows stay within action.MaxChanges")
	require.Len(t, changes, action.MaxChanges)
}
