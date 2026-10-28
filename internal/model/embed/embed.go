// Package embed embeds a list of texts with a model.Embedder: it cuts each
// text to a safe length, sends the texts in batches, retries a failed
// batch, and adds up the usage. The texts are memory text, which is
// redacted already; this package never logs them.
package embed

import (
	"context"
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits of one embedding call.
const (
	// MaxBatch is the most texts one call to the embedder holds.
	MaxBatch = 64
	// MaxTextBytes is the longest text sent; a longer one is cut.
	MaxTextBytes = 8000
)

// errWrongCount is returned when the embedder answers with a different
// number of vectors than the texts it was given.
var errWrongCount = errors.New("the embedder returned a different number of vectors than texts")

// Texts returns one vector for each of texts, in order, with the usage of
// all calls added up. Each text is cut to MaxTextBytes, and the texts go in
// batches of at most MaxBatch. A batch that fails with a retryable error is
// repeated as r says. A failure of any batch fails the whole call.
func Texts(ctx context.Context, e model.Embedder, r model.Retrier, texts []string) (model.EmbedResult, error) {
	out := model.EmbedResult{Vectors: make([][]float32, 0, len(texts))}

	for start := 0; start < len(texts); start += MaxBatch {
		batch := cutAll(texts[start:min(start+MaxBatch, len(texts))])

		res, err := r.Embed(ctx, e, batch)
		if err != nil {
			return model.EmbedResult{}, fmt.Errorf("embed texts %d to %d of %d: %w", start+1, start+len(batch), len(texts), err)
		}

		if len(res.Vectors) != len(batch) {
			return model.EmbedResult{}, &model.Error{
				Kind: model.ErrInvalid,
				Err:  fmt.Errorf("embed texts %d to %d of %d: %w: sent %d, got %d", start+1, start+len(batch), len(texts), errWrongCount, len(batch), len(res.Vectors)),
			}
		}

		out.Vectors = append(out.Vectors, res.Vectors...)
		out.Usage = out.Usage.Add(res.Usage)
		out.RequestID = res.RequestID
	}

	return out, nil
}

// cutAll returns texts with each one cut to MaxTextBytes.
func cutAll(texts []string) []string {
	cut := make([]string, len(texts))
	for i, t := range texts {
		cut[i] = source.CutText(t, MaxTextBytes)
	}

	return cut
}
