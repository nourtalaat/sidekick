package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/embed"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Limits of the vector search.
const (
	// fusionDepth is how many entries each list gives the rank fusion.
	fusionDepth = 20
	// nearestCandidates is how many entries the HNSW index returns before
	// the filters of a search apply. An index scan returns at most
	// hnsw.ef_search rows, 40 by default.
	nearestCandidates = 40
)

// errNoVectors is returned by a nil *Vectors that is asked to embed.
var errNoVectors = errors.New("vector memory is off")

// Vectors turns text into vectors and finds the memory entries nearest to
// them. A nil *Vectors means vector memory is off: every function that
// takes one then uses full-text search only. It is safe for concurrent use.
type Vectors struct {
	embedder model.Embedder
	retrier  model.Retrier
	model    string
	log      *slog.Logger
}

// NewVectors returns a Vectors that embeds with embedder, repeating a call
// that fails with a retryable error up to retries times. modelID is the
// embeddings model's ID, which each stored vector records, so a changed
// model embeds the entries again. A nil logger logs nothing.
func NewVectors(embedder model.Embedder, retries int, modelID string, logger *slog.Logger) *Vectors {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &Vectors{embedder: embedder, retrier: model.Retrier{Retries: retries}, model: modelID, log: logger}
}

// Model returns the ID of the embeddings model.
func (v *Vectors) Model() string {
	return v.model
}

// Dimensions returns the length of the vectors.
func (v *Vectors) Dimensions() int {
	return v.embedder.Dimensions()
}

// Nearest returns up to limit enabled entries nearest in meaning to text,
// nearest first, leaving out the investigations in exclude, and optionally
// only those of one service or with one verdict. When the embeddings call
// or the query fails it logs a warning and returns no entries, so a search
// falls back to full text; vectors never fail a search or an investigation.
func (v *Vectors) Nearest(ctx context.Context, q db.DBTX, text string, f NearestFilter, limit int32) []db.ListNearestMemoryRow {
	if v == nil {
		return nil
	}

	res, err := v.embedText(ctx, text)
	if err != nil {
		v.log.WarnContext(ctx, "vector memory: cannot embed the search text, using full text only", "error", err)

		return nil
	}

	rows, err := db.New(q).ListNearestMemory(ctx, db.ListNearestMemoryParams{
		Query:      FormatVector(res.Vectors[0]),
		Candidates: nearestCandidates,
		Exclude:    f.Exclude,
		Service:    f.Service,
		Verdict:    f.Verdict,
		MaxRows:    limit,
	})
	if err != nil {
		v.log.WarnContext(ctx, "vector memory: cannot search the embeddings, using full text only", "error", err)

		return nil
	}

	return rows
}

// NearestFilter narrows a vector search.
type NearestFilter struct {
	// Exclude are investigations that never match.
	Exclude []uuid.UUID
	// Service and Verdict, when set, keep only the entries of that service
	// or with that verdict.
	Service *string
	Verdict *string
}

// FormatVector returns v in the text form pgvector reads, such as
// "[0.5,1,-2]". pgx sends it as a string, which the vector type accepts.
func FormatVector(v []float32) string {
	var b strings.Builder

	b.WriteByte('[')

	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}

		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}

	b.WriteByte(']')

	return b.String()
}

// embedText embeds one text with the embeddings model, cut and retried as
// embed.Texts does.
func (v *Vectors) embedText(ctx context.Context, text string) (model.EmbedResult, error) {
	if v == nil {
		return model.EmbedResult{}, errNoVectors
	}

	res, err := embed.Texts(ctx, v.embedder, v.retrier, []string{text})
	if err != nil {
		return model.EmbedResult{}, fmt.Errorf("embed the text: %w", err)
	}

	return res, nil
}
