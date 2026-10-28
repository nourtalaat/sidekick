//go:build integration

package memory

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// testModel and testDimensions are the embeddings model and vector length
// of the vector tests.
const (
	testModel      = "test-model"
	testDimensions = 3
)

// The entries in fixture/vector.sql.
var (
	fusionF1 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f1")
	fusionF2 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f2")
	fusionF3 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f3")
	fusionF4 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f4")
	fusionF5 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f5")
)

// recordingEmbedder is a model.Embedder that records the texts it gets and
// answers each with the vector the vector function gives for the number of
// the call, counted from 1. A nil vector function answers [1,0,0]; err, when
// set, is returned instead.
type recordingEmbedder struct {
	vector func(call int) []float32
	err    error

	mu    sync.Mutex
	texts []string
}

func (e *recordingEmbedder) Embed(_ context.Context, texts []string) (model.EmbedResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.err != nil {
		return model.EmbedResult{}, e.err
	}

	e.texts = append(e.texts, texts...)

	res := model.EmbedResult{Usage: model.Usage{InputTokens: int64(len(texts))}}

	for range texts {
		v := []float32{1, 0, 0}
		if e.vector != nil {
			v = e.vector(len(e.texts))
		}

		res.Vectors = append(res.Vectors, v)
	}

	return res, nil
}

func (e *recordingEmbedder) Dimensions() int { return testDimensions }

// received returns the texts the embedder has got.
func (e *recordingEmbedder) received() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.texts...)
}

// TestExamplesFuseTheNearestEntries checks that the examples of a prompt
// come in the order of the reciprocal rank fusion of the full-text list and
// the vector list, worked out by hand.
//
// For the alert text "checkout latency deploy timeout" the full-text list
// is f1, f2, f3, f4. For the query vector [1,0,0] the vector list is f3,
// f1, f5, f2, f4. With k = 60, an entry scores 1/(60+rank) in each list it
// is in:
//
//	f1: 1/61 + 1/62 = 0.032522
//	f3: 1/63 + 1/61 = 0.032266
//	f2: 1/62 + 1/64 = 0.031754
//	f4: 1/64 + 1/65 = 0.031010
//	f5:         1/63 = 0.015873
func TestExamplesFuseTheNearestEntries(t *testing.T) {
	t.Parallel()

	pool := setupVector(t, testDimensions, "fixture/vector.sql")

	alert := Match{
		Investigation: uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f9"),
		Text:          "checkout latency deploy timeout",
	}

	tests := []struct {
		name    string
		vectors func() *Vectors
		limit   int
		want    []chosen
	}{
		{
			name:    "without vectors, the full-text order",
			vectors: func() *Vectors { return nil },
			limit:   MaxExamples,
			want: []chosen{
				{fusionF1, prompt.MatchText}, {fusionF2, prompt.MatchText}, {fusionF3, prompt.MatchText}, {fusionF4, prompt.MatchText},
			},
		},
		{
			name:    "with vectors, the fused order, and the entry only the vectors found",
			vectors: func() *Vectors { return NewVectors(&recordingEmbedder{}, 0, testModel, nil) },
			limit:   MaxExamples,
			want: []chosen{
				{fusionF1, prompt.MatchText}, {fusionF3, prompt.MatchText}, {fusionF2, prompt.MatchText},
				{fusionF4, prompt.MatchText}, {fusionF5, prompt.MatchSimilar},
			},
		},
		{
			name:    "the limit keeps the best of the fused order",
			vectors: func() *Vectors { return NewVectors(&recordingEmbedder{}, 0, testModel, nil) },
			limit:   3,
			want:    []chosen{{fusionF1, prompt.MatchText}, {fusionF3, prompt.MatchText}, {fusionF2, prompt.MatchText}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Examples(t.Context(), pool, alert, tt.limit, tt.vectors())
			require.NoError(t, err)

			var have []chosen
			for _, e := range got {
				have = append(have, chosen{uuid.MustParse(e.InvestigationID), e.Match})
			}

			require.Equal(t, tt.want, have)
		})
	}
}

// TestExamplesFallBackWhenTheEmbedderFails checks that a failing embeddings
// call leaves the examples to full text, logs a warning without the text,
// and does not fail.
func TestExamplesFallBackWhenTheEmbedderFails(t *testing.T) {
	t.Parallel()

	pool := setupVector(t, testDimensions, "fixture/vector.sql")

	var logs bytes.Buffer

	embedder := &recordingEmbedder{err: &model.Error{Kind: model.ErrOverloaded, Err: errors.New("503")}}
	vec := NewVectors(embedder, 0, testModel, slog.New(slog.NewTextHandler(&logs, nil)))

	got, err := Examples(t.Context(), pool, Match{Text: "checkout latency deploy timeout"}, MaxExamples, vec)
	require.NoError(t, err)

	var ids []uuid.UUID
	for _, e := range got {
		require.Equal(t, prompt.MatchText, e.Match)
		ids = append(ids, uuid.MustParse(e.InvestigationID))
	}

	require.Equal(t, []uuid.UUID{fusionF1, fusionF2, fusionF3, fusionF4}, ids)
	require.Contains(t, logs.String(), "level=WARN")
	require.Contains(t, logs.String(), "using full text only")
	require.NotContains(t, logs.String(), "checkout")
}

// TestEmbedMemoryJob follows a memory entry through the embedding job: a
// confirmation enqueues one job, the job stores the vector, a changed
// confirmation embeds the entry again, and a withdrawn one removes the row.
func TestEmbedMemoryJob(t *testing.T) {
	t.Parallel()

	pool := setupVector(t, testDimensions, "fixture/investigations.sql")

	// Each call answers [n,1,0], so a vector shows which call made it.
	embedder := &recordingEmbedder{vector: func(call int) []float32 { return []float32{float32(call), 1, 0} }}
	vec := NewVectors(embedder, 0, testModel, nil)

	syncEntry := func() {
		t.Helper()

		require.NoError(t, pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
			return Sync(t.Context(), tx, confirmedInvestigation, SyncOptions{Embed: true})
		}))
	}

	// A confirmation enqueues one job, however many times it is synced.
	// No worker runs yet, so the job stays pending.
	syncEntry()
	syncEntry()

	require.Equal(t, 1, countRows(t, pool, "SELECT count(*) FROM jobs WHERE kind = 'embed_memory' AND status = 'pending'"))

	runner, err := queue.NewRunner(pool, queue.Options{PollInterval: 20 * time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, runner.Register(queue.KindEmbedMemory, NewEmbedJob(pool, vec, nil), queue.KindOptions{}))

	ctx, cancel := context.WithCancel(t.Context())

	var wg sync.WaitGroup

	wg.Go(func() { _ = runner.Run(ctx) })

	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	waitForJobs(t, pool, 1)

	require.Equal(t, []string{"CheckoutLatencyP99\ncheckout-api\np99 latency rose after deploy 4f2c1a.\nDeploy 4f2c1a"}, embedder.received())
	require.Equal(t, "[1,1,0]", embeddingOf(t, pool))
	require.Equal(t, 1, countRows(t, pool, "SELECT count(*) FROM memory_embeddings WHERE model = 'test-model' AND dimensions = 3"))

	// A changed confirmation embeds the entry again.
	_, err = pool.Exec(t.Context(), "UPDATE investigations SET confirmed_cause = 'Pool exhaustion' WHERE id = $1", confirmedInvestigation)
	require.NoError(t, err)

	syncEntry()
	waitForJobs(t, pool, 2)

	require.Len(t, embedder.received(), 2)
	require.Contains(t, embedder.received()[1], "Pool exhaustion")
	require.Equal(t, "[2,1,0]", embeddingOf(t, pool))
	require.Equal(t, 1, countRows(t, pool, "SELECT count(*) FROM memory_embeddings"), "the vector was replaced, not added")

	// A withdrawn confirmation deletes the entry, and the row goes by
	// cascade, with no job.
	_, err = pool.Exec(t.Context(), `UPDATE investigations
		SET confirmed_verdict = NULL, confirmed_cause = NULL, confirmed_by_slack_user = NULL, confirmed_at = NULL WHERE id = $1`, confirmedInvestigation)
	require.NoError(t, err)

	syncEntry()

	require.Zero(t, countRows(t, pool, "SELECT count(*) FROM memory_embeddings"))
	require.Equal(t, 2, countRows(t, pool, "SELECT count(*) FROM jobs WHERE kind = 'embed_memory'"), "no job for a deleted entry")
}

func TestUpsertMemoryEmbeddingChecksTheText(t *testing.T) {
	t.Parallel()

	pool := setupVector(t, testDimensions, "fixture/investigations.sql")

	require.NoError(t, pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		return Sync(t.Context(), tx, confirmedInvestigation, SyncOptions{})
	}))

	queries := db.New(pool)

	row, err := queries.GetMemoryEmbedText(t.Context(), confirmedInvestigation)
	require.NoError(t, err)

	params := func(id uuid.UUID, hash string) db.UpsertMemoryEmbeddingParams {
		return db.UpsertMemoryEmbeddingParams{Model: testModel, Dimensions: testDimensions, Embedding: "[1,2,3]", InvestigationID: id, TextHash: hash}
	}

	tests := []struct {
		name string
		args db.UpsertMemoryEmbeddingParams
		want int64
	}{
		{name: "an entry that changed since its text was read", args: params(confirmedInvestigation, "stale"), want: 0},
		{name: "an entry that is gone", args: params(uuid.MustParse("0192d3a4-0000-7000-8000-0000000000cf"), row.TextHash), want: 0},
		{name: "an entry with the text that was embedded", args: params(confirmedInvestigation, row.TextHash), want: 1},
		{name: "the same entry again replaces the row", args: params(confirmedInvestigation, row.TextHash), want: 1},
	}

	// The cases run in order: the first two store nothing.
	for _, tt := range tests {
		n, err := queries.UpsertMemoryEmbedding(t.Context(), tt.args)
		require.NoError(t, err, tt.name)
		require.Equal(t, tt.want, n, tt.name)
	}

	require.Equal(t, 1, countRows(t, pool, "SELECT count(*) FROM memory_embeddings"))
}

func TestBackfill(t *testing.T) {
	t.Parallel()

	pool := setupVector(t, testDimensions, "fixture/entries.sql")

	// e1 has a vector from the current model, e2 from an old one.
	_, err := pool.Exec(t.Context(), `INSERT INTO memory_embeddings (investigation_id, model, dimensions, embedding, embedded_at)
		VALUES ('0192d3a4-0000-7000-8000-0000000000e1', 'test-model', 3, '[1,2,3]', now()),
		       ('0192d3a4-0000-7000-8000-0000000000e2', 'old-model', 3, '[1,2,3]', now())`)
	require.NoError(t, err)

	vec := NewVectors(&recordingEmbedder{}, 0, testModel, nil)

	// The fixture has eight entries; seven need an embedding. Disabled
	// ones are embedded too, in case an admin enables them again.
	for range 2 {
		n, err := Backfill(t.Context(), pool, vec)
		require.NoError(t, err)
		require.Equal(t, 7, n)

		require.Equal(t, 7, countRows(t, pool, "SELECT count(*) FROM jobs WHERE kind = 'embed_memory' AND status = 'pending'"),
			"an entry with a job is left to it")
	}

	require.Zero(t, countRows(t, pool, "SELECT count(*) FROM jobs WHERE kind = 'embed_memory' AND unique_key = 'embed_memory:0192d3a4-0000-7000-8000-0000000000e1'"))
	require.Equal(t, 1, countRows(t, pool, "SELECT count(*) FROM jobs WHERE kind = 'embed_memory' AND unique_key = 'embed_memory:0192d3a4-0000-7000-8000-0000000000e2'"),
		"an entry embedded by another model gets a job")
}

// countRows runs a query that returns one number.
func countRows(t *testing.T, pool *pgxpool.Pool, sql string) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), sql).Scan(&n))

	return n
}

// embeddingOf returns the vector of the confirmed investigation's entry.
func embeddingOf(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	var v string
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT embedding::text FROM memory_embeddings WHERE investigation_id = $1", confirmedInvestigation).Scan(&v))

	return v
}

// waitForJobs waits until n embed_memory jobs are done.
func waitForJobs(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()

	require.Eventually(t, func() bool {
		var done int

		err := pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'embed_memory' AND status = 'done'").Scan(&done)

		return err == nil && done == n
	}, 30*time.Second, 20*time.Millisecond)
}
