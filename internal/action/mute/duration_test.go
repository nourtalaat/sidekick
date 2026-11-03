package mute_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

func TestMaxDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured time.Duration
		want       time.Duration
		wantErr    string
	}{
		{name: "unset gives the default", want: mute.DefaultMaxDuration},
		{name: "the shortest", configured: mute.MinDuration, want: mute.MinDuration},
		{name: "just below the shortest", configured: mute.MinDuration - time.Second, wantErr: "below 5m"},
		{name: "the ceiling", configured: mute.CeilingMaxDuration, want: mute.CeilingMaxDuration},
		{name: "just above the ceiling", configured: mute.CeilingMaxDuration + time.Second, wantErr: "above 24h"},
		{name: "negative", configured: -time.Hour, wantErr: "below 5m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := mute.MaxDuration(tt.configured)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		limit   time.Duration
		want    time.Duration
		wantErr error
	}{
		{name: "the shortest", raw: "5m", limit: 2 * time.Hour, want: 5 * time.Minute},
		{name: "just below the shortest", raw: "4m59s", limit: 2 * time.Hour, wantErr: action.ErrLimit},
		{name: "the limit", raw: "2h", limit: 2 * time.Hour, want: 2 * time.Hour},
		{name: "just above the limit", raw: "2h0m1s", limit: 2 * time.Hour, wantErr: action.ErrLimit},
		{name: "a long form", raw: "90m", limit: 2 * time.Hour, want: 90 * time.Minute},
		{name: "negative", raw: "-1h", limit: 2 * time.Hour, wantErr: action.ErrLimit},
		{name: "not a duration", raw: "an hour", limit: 2 * time.Hour, wantErr: action.ErrParams},
		{name: "empty", raw: "", limit: 2 * time.Hour, wantErr: action.ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := mute.ParseDuration(tt.raw, tt.limit)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Minute, "5m"},
		{2 * time.Hour, "2h"},
		{90 * time.Minute, "1h30m"},
		{24 * time.Hour, "24h"},
		{45 * time.Second, "45s"},
		{time.Hour + 30*time.Second, "1h0m30s"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, mute.FormatDuration(tt.d))
		})
	}
}
