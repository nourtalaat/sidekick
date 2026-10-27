package investigator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSpendDay(t *testing.T) {
	t.Parallel()

	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)

	// 23:30 UTC on 26 October is 00:30 on 27 October in Berlin.
	at := time.Date(2026, 10, 26, 23, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		loc  *time.Location
		want time.Time
	}{
		{name: "UTC when no time zone is set", want: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)},
		{name: "UTC", loc: time.UTC, want: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)},
		{name: "a time zone already in the next day", loc: berlin, want: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			day := spendDay(at, tt.loc)
			require.True(t, day.Valid)
			require.Equal(t, tt.want, day.Time)
		})
	}
}
