package pricing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

// upliftPercent is the regional Bedrock price as a percentage of the
// global one.
const upliftPercent = 110

func TestBedrockCost(t *testing.T) {
	t.Parallel()

	usage := model.Usage{InputTokens: 2_000, OutputTokens: 1_000, CacheReadTokens: 100_000, CacheWriteTokens: 10_000}

	tests := []struct {
		name   string
		model  string
		usage  model.Usage
		want   int64
		wantOK bool
	}{
		{
			name:   "global profile at the Claude API price",
			model:  "global.anthropic.claude-opus-5-5",
			usage:  usage,
			want:   2_000*4 + 1_000*20 + 100_000*0.20 + 10_000*5,
			wantOK: true,
		},
		{
			name:   "geo profile at the regional price",
			model:  "us.anthropic.claude-opus-5-5",
			usage:  usage,
			want:   2_000*4.4 + 1_000*22 + 100_000*0.22 + 10_000*5.5,
			wantOK: true,
		},
		{
			name:   "bare model ID at the regional price",
			model:  "anthropic.claude-opus-5-5",
			usage:  usage,
			want:   2_000*4.4 + 1_000*22 + 100_000*0.22 + 10_000*5.5,
			wantOK: true,
		},
		{
			name:   "Haiku 5.5 on a geo profile with a long prompt uses the higher regional tier",
			model:  "eu.anthropic.claude-haiku-5-5",
			usage:  model.Usage{InputTokens: 100_001, OutputTokens: 1_000},
			want:   (100_001*550_000 + 1_000*2_750_000) / usd,
			wantOK: true,
		},
		{
			name:   "Haiku 5.5 on the global profile with a short prompt",
			model:  "global.anthropic.claude-haiku-5-5",
			usage:  model.Usage{InputTokens: 100_000, OutputTokens: 1_000},
			want:   100_000*0.10 + 1_000*0.50,
			wantOK: true,
		},
		{
			name:  "Mythos 5 has no global profile",
			model: "global.anthropic.claude-mythos-5",
			usage: usage,
		},
		{
			name:  "GovCloud profiles are not shipped",
			model: "us-gov.anthropic.claude-opus-5-5",
			usage: usage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := Shipped().Cost(tt.model, tt.usage)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestBedrockRegionalUplift checks that every regional price is the global
// price plus 10%, as the AWS price list has it.
func TestBedrockRegionalUplift(t *testing.T) {
	t.Parallel()

	for id, regional := range bedrockShipped {
		if strings.HasPrefix(id, bedrockGlobalPrefix) {
			continue
		}

		global, ok := bedrockShipped[bedrockGlobalPrefix+bareID(id)]
		if !ok {
			continue
		}

		t.Run(id, func(t *testing.T) {
			t.Parallel()

			requireUplift(t, global, regional)

			if regional.Above != nil {
				require.NotNil(t, global.Above)
				require.Equal(t, global.TierAboveInputTokens, regional.TierAboveInputTokens)
				requireUplift(t, *global.Above, *regional.Above)
			}
		})
	}
}

// requireUplift checks that each field of regional is global plus 10%.
func requireUplift(t *testing.T, global, regional model.Price) {
	t.Helper()

	require.Equal(t, global.InputPerMTok*upliftPercent/100, regional.InputPerMTok)
	require.Equal(t, global.OutputPerMTok*upliftPercent/100, regional.OutputPerMTok)
	require.Equal(t, global.CacheReadPerMTok*upliftPercent/100, regional.CacheReadPerMTok)
	require.Equal(t, global.CacheWritePerMTok*upliftPercent/100, regional.CacheWritePerMTok)
}

// bareID strips a geo profile prefix from a Bedrock ID.
func bareID(id string) string {
	for _, p := range bedrockGeoPrefixes {
		if rest, ok := strings.CutPrefix(id, p); ok {
			return rest
		}
	}

	return id
}
