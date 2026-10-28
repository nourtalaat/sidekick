package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestBuildEmbedder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		fixture      string
		wantEmbedder bool
		wantSettings model.Settings
	}{
		{name: "vector memory builds the embeddings connector", fixture: "valid/vector.yaml", wantEmbedder: true, wantSettings: model.Settings{Model: "voyage-4", Retries: 2}},
		{name: "without vector memory no embedder is built", fixture: "valid/minimal.yaml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := testOptions()

			cfg, err := Parse(t.Context(), readFixture(t, tt.fixture), opts)
			require.NoError(t, err)

			in, err := Build(t.Context(), cfg, opts.Registries, registry.Deps{})
			require.NoError(t, err)

			if !tt.wantEmbedder {
				require.Nil(t, in.Embedder)
				require.Zero(t, in.EmbedderSettings)

				return
			}

			require.NotNil(t, in.Embedder)
			require.Equal(t, 1024, in.Embedder.Dimensions())
			require.Equal(t, tt.wantSettings, in.EmbedderSettings)
		})
	}
}
