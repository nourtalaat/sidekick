package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStatsWindow(t *testing.T) {
	t.Parallel()

	w := statsWindow{
		from: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		to:   time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC),
	}

	require.Equal(t, 7, w.days())
	require.Equal(t, statsWindow{
		from: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		to:   time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
	}, w.before())
	require.Equal(t, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), w.end())
	require.Equal(t, 1, statsWindow{from: w.from, to: w.from}.days())
}
