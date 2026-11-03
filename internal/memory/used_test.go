package memory

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/prompt"
)

func TestExampleColumns(t *testing.T) {
	t.Parallel()

	first := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e1")
	second := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e2")

	tests := []struct {
		name        string
		examples    []prompt.Example
		wantIDs     []uuid.UUID
		wantMatches []string
		wantErr     bool
	}{
		{
			name: "the order of the examples is kept",
			examples: []prompt.Example{
				{InvestigationID: second.String(), Match: prompt.MatchText},
				{InvestigationID: first.String(), Match: prompt.MatchFingerprint},
			},
			wantIDs:     []uuid.UUID{second, first},
			wantMatches: []string{"text", "fingerprint"},
		},
		{
			name:     "an ID that is not a UUID is refused",
			examples: []prompt.Example{{InvestigationID: "not-an-id", Match: prompt.MatchText}},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ids, matches, err := exampleColumns(tt.examples)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantIDs, ids)
			require.Equal(t, tt.wantMatches, matches)
		})
	}
}
