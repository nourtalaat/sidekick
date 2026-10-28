package memory

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/queue"
)

func TestFormatVector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []float32
		want string
	}{
		{name: "whole and fractional numbers", in: []float32{1, 0.5, -2.25}, want: "[1,0.5,-2.25]"},
		{name: "a number float32 cannot hold exactly", in: []float32{0.1}, want: "[0.1]"},
		{name: "a small number", in: []float32{1e-7}, want: "[1e-07]"},
		{name: "one number", in: []float32{3}, want: "[3]"},
		{name: "empty", in: nil, want: "[]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, FormatVector(tt.in))
		})
	}
}

// TestNearestFallsBack checks that a failing embeddings call, and a nil
// Vectors, give no entries instead of an error, and that the warning never
// holds the text.
func TestNearestFallsBack(t *testing.T) {
	t.Parallel()

	const secretText = "the password is hunter2"

	tests := []struct {
		name     string
		vec      func(t *testing.T, logs *bytes.Buffer) *Vectors
		wantWarn bool
	}{
		{name: "vectors are off", vec: func(*testing.T, *bytes.Buffer) *Vectors { return nil }},
		{
			name: "the embeddings call fails",
			vec: func(t *testing.T, logs *bytes.Buffer) *Vectors {
				t.Helper()

				e := model.NewMockEmbedder(gomock.NewController(t))
				e.EXPECT().Embed(gomock.Any(), []string{secretText}).Return(model.EmbedResult{}, &model.Error{Kind: model.ErrAuth, Err: errors.New("401")})

				return NewVectors(e, 0, "test-model", slog.New(slog.NewTextHandler(logs, nil)))
			},
			wantWarn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			got := tt.vec(t, &logs).Nearest(t.Context(), nil, secretText, NearestFilter{}, fusionDepth)
			require.Empty(t, got)

			if !tt.wantWarn {
				require.Empty(t, logs.String())

				return
			}

			require.Contains(t, logs.String(), "level=WARN")
			require.Contains(t, logs.String(), "using full text only")
			require.NotContains(t, logs.String(), "hunter2")
		})
	}
}

func TestGiveUpIfInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantGiveUp bool
	}{
		{name: "an invalid request", err: &model.Error{Kind: model.ErrInvalid, Err: errors.New("bad")}, wantGiveUp: true},
		{name: "a text that is too long", err: &model.Error{Kind: model.ErrContextLength, Err: errors.New("long")}, wantGiveUp: true},
		{name: "a rate limit is repeated", err: &model.Error{Kind: model.ErrRateLimited, Err: errors.New("429")}},
		{name: "a bad key is repeated, after the operator fixes it", err: &model.Error{Kind: model.ErrAuth, Err: errors.New("401")}},
		{name: "an error that is not a model error", err: errors.New("boom")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := giveUpIfInvalid(tt.err)
			require.ErrorIs(t, got, tt.err)
			require.Equal(t, tt.wantGiveUp, errors.Is(got, queue.ErrGiveUp))
		})
	}
}
