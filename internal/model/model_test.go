package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilitiesSupportsThinking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		caps Capabilities
		mode ThinkingMode
		want bool
	}{
		{name: "off is always supported", caps: Capabilities{}, mode: ThinkingOff, want: true},
		{name: "an empty mode means off", caps: Capabilities{}, mode: "", want: true},
		{name: "listed mode", caps: Capabilities{ThinkingModes: []ThinkingMode{ThinkingAdaptive}}, mode: ThinkingAdaptive, want: true},
		{name: "mode not listed", caps: Capabilities{ThinkingModes: []ThinkingMode{ThinkingAdaptive}}, mode: ThinkingEnabled, want: false},
		{name: "no modes listed", caps: Capabilities{}, mode: ThinkingEnabled, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.caps.SupportsThinking(tt.mode))
		})
	}
}

func TestCapabilitiesSupportsEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		caps  Capabilities
		level Effort
		want  bool
	}{
		{name: "default is always supported", caps: Capabilities{}, level: EffortDefault, want: true},
		{name: "listed level", caps: Capabilities{EffortLevels: []Effort{EffortLow, EffortHigh}}, level: EffortHigh, want: true},
		{name: "level not listed", caps: Capabilities{EffortLevels: []Effort{EffortLow, EffortHigh}}, level: EffortMax, want: false},
		{name: "no levels listed", caps: Capabilities{}, level: EffortLow, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.caps.SupportsEffort(tt.level))
		})
	}
}

func TestUsageAdd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    Usage
		b    Usage
		want Usage
	}{
		{name: "zero plus zero", want: Usage{}},
		{
			name: "every field is summed",
			a:    Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4, ReasoningTokens: 5},
			b:    Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 40, ReasoningTokens: 50},
			want: Usage{InputTokens: 11, OutputTokens: 22, CacheReadTokens: 33, CacheWriteTokens: 44, ReasoningTokens: 55},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.a.Add(tt.b))
			require.Equal(t, tt.want, tt.b.Add(tt.a))
		})
	}
}

func TestError(t *testing.T) {
	t.Parallel()

	cause := errors.New("HTTP 429")

	tests := []struct {
		kind          ErrorKind
		wantRetryable bool
	}{
		{kind: ErrRateLimited, wantRetryable: true},
		{kind: ErrOverloaded, wantRetryable: true},
		{kind: ErrTimeout, wantRetryable: true},
		{kind: ErrInvalid, wantRetryable: false},
		{kind: ErrAuth, wantRetryable: false},
		{kind: ErrContextLength, wantRetryable: false},
		{kind: ErrUnknown, wantRetryable: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			t.Parallel()

			var err error = &Error{Kind: tt.kind, Err: cause}

			require.EqualError(t, err, "model "+string(tt.kind)+": HTTP 429")
			require.ErrorIs(t, err, cause)

			var modelErr *Error
			require.ErrorAs(t, err, &modelErr)
			require.Equal(t, tt.wantRetryable, modelErr.Retryable())
		})
	}
}

func TestPriceCost(t *testing.T) {
	t.Parallel()

	sonnet := Price{InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, CacheReadPerMTok: 300_000, CacheWritePerMTok: 3_750_000}
	tiered := Price{
		InputPerMTok: 100_000, OutputPerMTok: 500_000, CacheReadPerMTok: 10_000, CacheWritePerMTok: 125_000,
		TierAboveInputTokens: 100_000,
		Above:                &Price{InputPerMTok: 500_000, OutputPerMTok: 2_500_000, CacheReadPerMTok: 50_000, CacheWritePerMTok: 625_000},
	}

	tests := []struct {
		name  string
		price Price
		usage Usage
		want  int64
	}{
		{name: "no usage costs nothing", price: sonnet, usage: Usage{}, want: 0},
		{name: "one million input tokens", price: sonnet, usage: Usage{InputTokens: 1_000_000}, want: 3_000_000},
		{name: "output tokens", price: sonnet, usage: Usage{OutputTokens: 2_000}, want: 30_000},
		{name: "cache reads and writes", price: sonnet, usage: Usage{CacheReadTokens: 10_000, CacheWriteTokens: 10_000}, want: 3_000 + 37_500},
		{
			name:  "reasoning tokens are not billed again, they are part of output",
			price: sonnet,
			usage: Usage{OutputTokens: 1_000, ReasoningTokens: 800},
			want:  15_000,
		},
		{
			name:  "the total is rounded down once, not each field",
			price: Price{InputPerMTok: 1, OutputPerMTok: 1},
			usage: Usage{InputTokens: 600_000, OutputTokens: 600_000},
			want:  1,
		},
		{name: "free model", price: Price{}, usage: Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, want: 0},
		{
			name:  "a short prompt uses the base tier",
			price: tiered,
			usage: Usage{InputTokens: 50_000, OutputTokens: 1_000},
			want:  5_000 + 500,
		},
		{
			name:  "a prompt exactly at the threshold uses the base tier",
			price: tiered,
			usage: Usage{InputTokens: 40_000, CacheReadTokens: 50_000, CacheWriteTokens: 10_000, OutputTokens: 1_000},
			want:  4_000 + 500 + 1_250 + 500,
		},
		{
			name:  "one token over the threshold prices every token at the higher tier",
			price: tiered,
			usage: Usage{InputTokens: 40_001, CacheReadTokens: 50_000, CacheWriteTokens: 10_000, OutputTokens: 1_000},
			want:  (40_001*500_000 + 50_000*50_000 + 10_000*625_000 + 1_000*2_500_000) / 1_000_000,
		},
		{
			name:  "cache reads count toward the threshold",
			price: tiered,
			usage: Usage{InputTokens: 1_000, CacheReadTokens: 150_000},
			want:  (1_000*500_000 + 150_000*50_000) / 1_000_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.price.Cost(tt.usage))
		})
	}
}

func TestCheckRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		role    Role
		caps    Capabilities
		wantErr error
	}{
		{name: "investigate with tool calling", role: RoleInvestigate, caps: Capabilities{ToolCalling: true}},
		{name: "investigate without tool calling", role: RoleInvestigate, caps: Capabilities{}, wantErr: ErrNoToolCalling},
		{name: "triage with tool calling", role: RoleTriage, caps: Capabilities{ToolCalling: true}},
		{name: "triage without tool calling", role: RoleTriage, caps: Capabilities{}, wantErr: ErrNoToolCalling},
		{name: "judge without tool calling", role: RoleJudge, caps: Capabilities{}},
		{name: "summarize without tool calling", role: RoleSummarize, caps: Capabilities{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckRole(tt.role, tt.caps)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorContains(t, err, string(tt.role))

				return
			}

			require.NoError(t, err)
		})
	}
}
