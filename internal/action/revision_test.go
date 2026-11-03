package action

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRevisionSnapshotRoundTrips(t *testing.T) {
	t.Parallel()

	deployed := time.Date(2026, 11, 3, 14, 2, 0, 0, time.UTC)
	running := Revision{Kind: "ReplicaSet revision", ID: "7", Version: "checkout:1.4.2", DeployedAt: &deployed, DeployedBy: "ana", Notes: "app=checkout:1.4.2"}
	back := Revision{Kind: "ReplicaSet revision", ID: "6"}

	snap, err := RevisionSnapshot("kubernetes:deployment/shop/checkout", "7", running, back)
	require.NoError(t, err)
	require.Equal(t, "kubernetes:deployment/shop/checkout", snap.Target)
	require.Equal(t, "7", snap.Version)
	require.JSONEq(t, `{"sidekickKind":"revision","kind":"ReplicaSet revision","id":"6"}`, string(snap.After))
	require.Contains(t, string(snap.Before), `"sidekickKind":"revision"`)

	got, err := snap.Revisions()
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Running.DeployedAt.Equal(deployed))

	got.Running.DeployedAt = running.DeployedAt
	require.Equal(t, &Revisions{Running: running, GoesBackTo: back}, got)
}

func TestRevisionSnapshotRefusesARevisionWithoutKindOrID(t *testing.T) {
	t.Parallel()

	ok := Revision{Kind: "task definition", ID: "checkout:42"}

	tests := []struct {
		name          string
		running, back Revision
	}{
		{name: "running without a kind", running: Revision{ID: "1"}, back: ok},
		{name: "running without an ID", running: Revision{Kind: "k"}, back: ok},
		{name: "target without a kind", running: ok, back: Revision{ID: "1"}},
		{name: "target without an ID", running: ok, back: Revision{Kind: "k"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := RevisionSnapshot("t", "v", tt.running, tt.back)
			require.Error(t, err)
		})
	}
}

func TestSnapshotRevisions(t *testing.T) {
	t.Parallel()

	const revision = `{"sidekickKind":"revision","kind":"k","id":"1"}`

	tests := []struct {
		name          string
		before, after string
		want          *Revisions
		wantErr       bool
	}{
		{name: "a normal definition", before: `{"a":1}`, after: `{"a":2}`},
		{name: "no definition on either side", before: ``, after: `null`},
		{name: "not JSON", before: `{`, after: `[`},
		{name: "a marker with another value", before: `{"sidekickKind":"flag"}`, after: `{"sidekickKind":"flag"}`},
		{name: "two revisions", before: revision, after: revision, want: &Revisions{Running: Revision{Kind: "k", ID: "1"}, GoesBackTo: Revision{Kind: "k", ID: "1"}}},
		{name: "only the before side", before: revision, after: `{"a":1}`, wantErr: true},
		{name: "a revision and a removal", before: revision, after: `null`, wantErr: true},
		{name: "only the after side", before: `null`, after: revision, wantErr: true},
		{name: "no ID", before: `{"sidekickKind":"revision","kind":"k"}`, after: revision, wantErr: true},
		{name: "a field of the wrong type", before: `{"sidekickKind":"revision","kind":1,"id":"1"}`, after: revision, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Snapshot{Before: json.RawMessage(tt.before), After: json.RawMessage(tt.after)}.Revisions()
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRevisionsDisplay(t *testing.T) {
	t.Parallel()

	deployed := time.Date(2026, 11, 3, 14, 2, 0, 0, time.UTC)
	rlo, zwsp := string(rune(0x202e)), string(rune(0x200b))

	got := Revisions{
		Running:    Revision{Kind: "image" + rlo + "tag", ID: "7", Version: "v1" + zwsp + "2", DeployedAt: &deployed, DeployedBy: "a`b", Notes: "one\ntwo" + bs + "u0041"},
		GoesBackTo: Revision{Kind: "plain", ID: "6", Version: longText()},
	}.Display()

	require.Equal(t, "image"+bs+"u202etag", got.Running.Kind)
	require.Equal(t, "v1"+bs+"u200b2", got.Running.Version)
	require.Equal(t, "a"+bs+"u0060b", got.Running.DeployedBy)
	require.Equal(t, "one"+bs+"ntwo"+bs+bs+"u0041", got.Running.Notes)
	require.Equal(t, &deployed, got.Running.DeployedAt)
	require.Equal(t, longText(), got.GoesBackTo.Version, "nothing is cut")
	require.Empty(t, got.GoesBackTo.DeployedBy)
	require.Nil(t, got.GoesBackTo.DeployedAt)
}

// bs is one backslash, so a test can write the text of an escape.
const bs = "\\"

func longText() string {
	return strings.Repeat("abcdefghij", 400)
}
