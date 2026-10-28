//go:build integration

package memory

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

func TestSearchLinksToTheInvestigationPage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		publicURL string
		want      string
	}{
		{name: "a public URL", publicURL: "https://sidekick.example.com", want: "https://sidekick.example.com/investigations/" + entryDeploy},
		{name: "no public URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			storetest.LoadFixture(t, pool, "fixture/memory.sql")

			src, err := New(t.Context(), "memory", Config{}, registry.Deps{DB: pool, PublicURL: tt.publicURL})
			require.NoError(t, err)

			res, err := src.Call(t.Context(), source.Call{ID: "c1", Tool: toolSearch, Input: json.RawMessage(`{"query":"deploy slow query"}`)})
			require.NoError(t, err)
			require.Len(t, res.Evidence, 1)
			require.Equal(t, tt.want, res.Evidence[0].URL)
		})
	}
}
