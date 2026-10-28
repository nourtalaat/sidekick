//go:build integration

package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// fakeEmbeddingConfig is the config of the fake voyage provider, which
// embeds text.
type fakeEmbeddingConfig struct {
	model.Settings `yaml:",inline"`

	Dimensions int `yaml:"dimensions"`
}

// Embedding implements model.EmbeddingConfigured.
func (c *fakeEmbeddingConfig) Embedding() (dimensions int, ok bool) {
	return c.Dimensions, true
}

// fakeEmbedder is the connector built from a fakeEmbeddingConfig.
type fakeEmbedder struct {
	fakeConnector

	dimensions int
}

func (fakeEmbedder) Embed(context.Context, []string) (model.EmbedResult, error) {
	return model.EmbedResult{}, errFakeGenerate
}

func (f fakeEmbedder) Dimensions() int { return f.dimensions }

// newVectorApp makes an App with vector memory on, for vectors of the given
// length, on the database at dbURL.
func newVectorApp(t *testing.T, dbURL string, mode config.MigrationMode, dimensions int) (*App, error) {
	t.Helper()

	path := writeConfig(t, dbURL, mode)
	appendConfig(t, path, fmt.Sprintf("memory:\n  vector:\n    enabled: true\n    embeddings: { provider: voyage, model: voyage-4, dimensions: %d }\n", dimensions))

	regs := fakeRegistries()
	regs.Models.Register("voyage", registry.Typed(func(_ context.Context, _ string, cfg fakeEmbeddingConfig, _ registry.Deps) (model.Connector, error) {
		return fakeEmbedder{dimensions: cfg.Dimensions}, nil
	}))

	a, err := New(t.Context(), Options{ConfigPath: path, Registries: regs, Build: testBuild, LogOutput: &syncBuffer{}})
	if err == nil {
		t.Cleanup(func() { require.NoError(t, a.Close()) })
	}

	return a, err
}

// migrateVectorDatabase applies the main and the vector migrations to the
// database at dbURL, as `sidekick migrate up` does.
func migrateVectorDatabase(t *testing.T, dbURL string, dimensions int) {
	t.Helper()

	pool, err := pgxpool.New(t.Context(), dbURL)
	require.NoError(t, err)

	defer pool.Close()

	require.NoError(t, store.Migrate(t.Context(), pool))
	require.NoError(t, store.MigrateVector(t.Context(), pool, dimensions))
}

func TestVectorMemoryMigratesAtStartup(t *testing.T) {
	t.Parallel()

	dbURL := storetest.NewVectorEmptyURL(t)

	a, err := newVectorApp(t, dbURL, config.MigrationsStartup, 3)
	require.NoError(t, err)

	current, target, err := store.VectorSchemaVersion(t.Context(), a.Pool())
	require.NoError(t, err)
	require.Equal(t, target, current)
	require.NoError(t, store.CheckVectorDimensions(t.Context(), a.Pool(), 3))
	require.Equal(t, 3, a.Config().Memory.Vector.Dimensions)
	require.NotNil(t, a.Instances().Embedder)

	// Another start with another length is refused.
	_, err = newVectorApp(t, dbURL, config.MigrationsStartup, 4)

	var dimErr *store.VectorDimensionsError
	require.ErrorAs(t, err, &dimErr)
	require.ErrorContains(t, err, "sidekick migrate vector-reset")
}

func TestVectorMemoryChecksTheLengthInManualMode(t *testing.T) {
	t.Parallel()

	dbURL := storetest.NewVectorEmptyURL(t)
	migrateVectorDatabase(t, dbURL, 3)

	_, err := newVectorApp(t, dbURL, config.MigrationsManual, 3)
	require.NoError(t, err)

	_, err = newVectorApp(t, dbURL, config.MigrationsManual, 4)

	var dimErr *store.VectorDimensionsError
	require.ErrorAs(t, err, &dimErr)
}

func TestVectorMemoryNotReadyUntilItsMigrationsRun(t *testing.T) {
	t.Parallel()

	dbURL := storetest.NewVectorEmptyURL(t)
	migrateDatabase(t, dbURL)

	a, err := newVectorApp(t, dbURL, config.MigrationsManual, 3)
	require.NoError(t, err, "a replica that does not migrate starts, and says it is not ready")

	got := readiness{cfg: a.Config(), pool: a.Pool()}.checkSchema(t.Context())
	require.False(t, got.ok)
	require.Contains(t, got.message, "vector memory migrations up to 0 but this release needs 1")
	require.Contains(t, got.message, "sidekick migrate up")

	migrateVectorDatabase(t, dbURL, 3)

	got = readiness{cfg: a.Config(), pool: a.Pool()}.checkSchema(t.Context())
	require.True(t, got.ok, got.message)
}
