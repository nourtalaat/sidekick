package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
)

func TestOpenRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		url      string
		maxConns int32
		wantErr  string
	}{
		{
			name:     "zero max connections",
			url:      "postgres://localhost:5432/sidekick",
			maxConns: 0,
			wantErr:  "max connections is 0, want at least 1",
		},
		{
			name:     "negative max connections",
			url:      "postgres://localhost:5432/sidekick",
			maxConns: -1,
			wantErr:  "max connections is -1, want at least 1",
		},
		{
			name:     "URL that cannot be parsed",
			url:      "postgres://localhost:notaport/sidekick",
			maxConns: 1,
			wantErr:  "parse database URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, err := store.Open(t.Context(), tt.url, tt.maxConns)
			require.ErrorContains(t, err, tt.wantErr)
			require.Nil(t, pool)
		})
	}
}
