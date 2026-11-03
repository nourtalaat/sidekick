package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// fakeReader is a note action that can also read files from a host.
type fakeReader struct {
	*fakeNote
}

func (fakeReader) ReadBlob(context.Context, json.RawMessage, string) (string, error) {
	return "", nil
}

func TestServiceReadsFiles(t *testing.T) {
	t.Parallel()

	svc, err := New(Options{
		Pool:   &pgxpool.Pool{},
		Policy: action.StaticPolicy{},
		Actions: map[string]action.Action{
			"reader": fakeReader{newFakeNote("reader")},
			"note":   newFakeNote("note"),
		},
	})
	require.NoError(t, err)

	require.True(t, svc.ReadsFiles("reader"))
	require.False(t, svc.ReadsFiles("note"), "an action that cannot read files")
	require.False(t, svc.ReadsFiles("gone"), "an action that is not configured")
}

func TestProposedContent(t *testing.T) {
	t.Parallel()

	const (
		path     = "svc/config.yaml"
		afterSHA = "1111111111111111111111111111111111111111"
	)

	snapshot := &action.Snapshot{
		Before: json.RawMessage(`{"svc/config.yaml": {"blob": "2222222222222222222222222222222222222222"}}`),
		After:  json.RawMessage(`{"svc/config.yaml": {"blob": "` + afterSHA + `", "status": "changed"}}`),
	}
	withFiles := json.RawMessage(`{"repo": "acme/svc", "files": [{"path": "svc/config.yaml", "content": "from params\n"}]}`)
	noFiles := json.RawMessage(`{"repo": "acme/svc", "commit": "abc"}`)

	tests := []struct {
		name       string
		params     json.RawMessage
		status     string
		snapshot   *action.Snapshot
		setupMocks func(r *action.MockFileReader)
		want       string
		wantErr    string
	}{
		{
			name: "content in the parameters is used and nothing is read", params: withFiles, status: gitchange.FileChanged,
			snapshot: snapshot, want: "from params\n",
		},
		{
			name: "a deleted file has no content", params: noFiles, status: gitchange.FileDeleted,
			snapshot: snapshot, want: "",
		},
		{
			name: "no files in the parameters reads the after blob", params: noFiles, status: gitchange.FileChanged, snapshot: snapshot,
			setupMocks: func(r *action.MockFileReader) {
				r.EXPECT().ReadBlob(gomock.Any(), noFiles, afterSHA).Return("from blob\n", nil)
			},
			want: "from blob\n",
		},
		{
			name: "an unreadable after blob", params: noFiles, status: gitchange.FileAdded, snapshot: snapshot,
			setupMocks: func(r *action.MockFileReader) {
				r.EXPECT().ReadBlob(gomock.Any(), noFiles, afterSHA).Return("", errors.New("no such blob"))
			},
			wantErr: "read the base of the file: no such blob",
		},
		{
			name: "a snapshot without a new blob", params: noFiles, status: gitchange.FileChanged,
			snapshot: &action.Snapshot{Before: json.RawMessage(`{}`), After: json.RawMessage(`{"svc/config.yaml": {"status": "changed"}}`)},
			wantErr:  "names no new blob",
		},
		{
			name: "parameters with files but not this path", params: json.RawMessage(`{"files": [{"path": "other", "content": "x"}]}`),
			status: gitchange.FileChanged, snapshot: snapshot, wantErr: "the parameters hold no content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := action.NewMockFileReader(gomock.NewController(t))
			if tt.setupMocks != nil {
				tt.setupMocks(reader)
			}

			v := View{Proposal: action.Proposal{Params: tt.params, Snapshot: tt.snapshot}}

			got, err := proposedContent(t.Context(), reader, v, action.ProposalFile{Path: path, Status: tt.status})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
