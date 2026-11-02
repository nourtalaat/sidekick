package daily

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestDayBounds(t *testing.T) {
	t.Parallel()

	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)

	tests := []struct {
		name      string
		now       time.Time
		loc       *time.Location
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "UTC when no zone is set",
			now:       time.Date(2026, 11, 2, 15, 30, 0, 0, time.UTC),
			wantStart: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "the day of the zone, not of UTC",
			now:       time.Date(2026, 11, 2, 23, 30, 0, 0, time.UTC),
			loc:       berlin,
			wantStart: time.Date(2026, 11, 3, 0, 0, 0, 0, berlin),
			wantEnd:   time.Date(2026, 11, 4, 0, 0, 0, 0, berlin),
		},
		{
			name:      "a day that is 25 hours long",
			now:       time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
			loc:       berlin,
			wantStart: time.Date(2026, 10, 25, 0, 0, 0, 0, berlin),
			wantEnd:   time.Date(2026, 10, 26, 0, 0, 0, 0, berlin),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			start, end := dayBounds(tt.now, tt.loc)
			require.True(t, tt.wantStart.Equal(start), "start %s", start)
			require.True(t, tt.wantEnd.Equal(end), "end %s", end)
		})
	}

	start, end := dayBounds(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), berlin)
	require.Equal(t, 25*time.Hour, end.Sub(start))
}

func TestCheckRefusesWhatCannotBeCounted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		counter Counter
		id      string
		max     int
		wantErr error
	}{
		{name: "no limit", counter: Counter{}, id: "0192d3a4-0000-7000-8000-0000000000f1", max: 0, wantErr: errBadMax},
		{name: "no database", counter: Counter{}, id: "0192d3a4-0000-7000-8000-0000000000f1", max: 1, wantErr: errNoDB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.counter.Check(t.Context(), "tickets", tt.id, tt.max)
			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrPermanent)
		})
	}
}
