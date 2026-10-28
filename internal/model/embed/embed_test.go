package embed

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/model"
)

func TestTexts(t *testing.T) {
	t.Parallel()

	overloaded := &model.Error{Kind: model.ErrOverloaded, Err: errors.New("503")}

	// vectors returns one vector for each text, holding its length, so a
	// test can tell which text went where.
	vectors := func(texts []string) model.EmbedResult {
		res := model.EmbedResult{Usage: model.Usage{InputTokens: int64(len(texts))}, RequestID: "req"}
		for _, text := range texts {
			res.Vectors = append(res.Vectors, []float32{float32(len(text))})
		}

		return res
	}

	tests := []struct {
		name      string
		texts     []string
		wantSizes []int
		// failFirst makes the first call of the first batch fail with a retryable error.
		failFirst bool
		// short makes the embedder drop one vector of its first answer.
		short       bool
		wantErr     error
		wantVectors [][]float32
		wantTokens  int64
	}{
		{
			name:        "texts that fit one batch",
			texts:       []string{"a", "bb"},
			wantSizes:   []int{2},
			wantVectors: [][]float32{{1}, {2}},
			wantTokens:  2,
		},
		{
			name:        "a text over the limit is cut",
			texts:       []string{strings.Repeat("x", MaxTextBytes+500)},
			wantSizes:   []int{1},
			wantVectors: [][]float32{{MaxTextBytes}},
			wantTokens:  1,
		},
		{
			name:       "more than one batch of texts",
			texts:      make([]string, MaxBatch+3),
			wantSizes:  []int{MaxBatch, 3},
			wantTokens: MaxBatch + 3,
		},
		{
			name:        "a retryable failure repeats the batch",
			texts:       []string{"a"},
			failFirst:   true,
			wantSizes:   []int{1, 1},
			wantVectors: [][]float32{{1}},
			wantTokens:  1,
		},
		{
			name:      "a missing vector is an invalid request",
			texts:     []string{"a", "b"},
			short:     true,
			wantSizes: []int{2},
			wantErr:   errWrongCount,
		},
		{
			name:        "no texts make no call",
			texts:       nil,
			wantVectors: [][]float32{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			e := model.NewMockEmbedder(ctrl)

			var sizes []int

			first := true

			e.EXPECT().Embed(gomock.Any(), gomock.Any()).DoAndReturn(func(_ any, batch []string) (model.EmbedResult, error) {
				sizes = append(sizes, len(batch))

				if tt.failFirst && first {
					first = false

					return model.EmbedResult{}, overloaded
				}

				res := vectors(batch)
				if tt.short && first {
					res.Vectors = res.Vectors[1:]
				}

				first = false

				return res, nil
			}).AnyTimes()

			got, err := Texts(t.Context(), e, model.Retrier{Retries: 1, BaseDelay: 1}, tt.texts)
			require.Equal(t, tt.wantSizes, sizes)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				var merr *model.Error
				require.ErrorAs(t, err, &merr)
				require.Equal(t, model.ErrInvalid, merr.Kind)

				return
			}

			require.NoError(t, err)
			require.Len(t, got.Vectors, len(tt.texts))
			require.Equal(t, tt.wantTokens, got.Usage.InputTokens)

			if tt.wantVectors != nil {
				require.Equal(t, tt.wantVectors, got.Vectors)
			}
		})
	}
}

func TestTextsFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	e := model.NewMockEmbedder(ctrl)
	auth := &model.Error{Kind: model.ErrAuth, Err: errors.New("401")}
	e.EXPECT().Embed(gomock.Any(), gomock.Any()).Return(model.EmbedResult{}, auth)

	_, err := Texts(t.Context(), e, model.Retrier{Retries: 3}, []string{"a"})
	require.ErrorIs(t, err, auth)
}
