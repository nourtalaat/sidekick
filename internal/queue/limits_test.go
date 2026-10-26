package queue

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLimitsParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limits  Limits
		want    limitParams
		wantErr string
	}{
		{
			name:   "global only",
			limits: Limits{Global: 8},
			want:   limitParams{global: 8, routes: []string{}, routeLimits: []int32{}},
		},
		{
			name:   "routes in a fixed order",
			limits: Limits{Global: 8, Routes: map[string]int{"payments": 3, "checkout": 1, "search": 0}},
			want: limitParams{
				global:      8,
				routes:      []string{"checkout", "payments", "search"},
				routeLimits: []int32{1, 3, 0},
			},
		},
		{name: "no global limit", limits: Limits{}, wantErr: "global limit is 0, want 1 to 1048576"},
		{name: "global limit too large", limits: Limits{Global: maxLimit + 1}, wantErr: "global limit is 1048577"},
		{
			name:    "negative route limit",
			limits:  Limits{Global: 8, Routes: map[string]int{"payments": -1}},
			wantErr: `limit of route "payments" is -1, want 0 to 1048576`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.limits.params()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
