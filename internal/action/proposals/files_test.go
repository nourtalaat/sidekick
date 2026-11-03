package proposals

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
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
