//go:build integration

package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
)

func TestStore(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		d    Delivery
		// cancel ends the context before Store runs.
		cancel  bool
		wantErr bool
		check   func(t *testing.T, h *harness, stored []Stored)
	}{
		{
			name: "new events are stored with their route_event jobs",
			d: Delivery{Intake: "alerts", Trusted: true, ReceivedAt: at, Events: []event.Event{
				{Key: "a", Kind: event.KindAlert, Title: "One"},
				{Key: "b", Kind: event.KindMessage, Title: "Two"},
			}},
			check: func(t *testing.T, h *harness, stored []Stored) {
				t.Helper()

				require.Len(t, stored, 2)
				events := h.events(t)
				require.Len(t, events, 3)

				for i, s := range stored {
					ev := events[i+1]
					require.Equal(t, ev.ID, s.ID)
					require.Equal(t, ev.Key, s.Key)
					require.Equal(t, StatusReceived, s.Status)
					require.False(t, s.Duplicate)
					require.True(t, ev.Trusted)
					require.Equal(t, at, ev.ReceivedAt.UTC())
				}

				require.Equal(t, []string{stored[0].ID.String(), stored[1].ID.String()}, h.routeJobs(t))
			},
		},
		{
			name: "a key the intake already has is a duplicate",
			d: Delivery{Intake: "alerts", ReceivedAt: at, Events: []event.Event{
				{Key: "seen", Kind: event.KindAlert, Title: "Again"},
				{Key: "new", Kind: event.KindAlert, Title: "New"},
			}},
			check: func(t *testing.T, h *harness, stored []Stored) {
				t.Helper()

				require.Equal(t, Stored{Key: "seen", Duplicate: true}, stored[0])
				require.False(t, stored[1].Duplicate)
				require.Len(t, h.events(t), 2)
				require.Equal(t, []string{stored[1].ID.String()}, h.routeJobs(t))
			},
		},
		{
			name: "the same key on another intake is a new event",
			d:    Delivery{Intake: "other", ReceivedAt: at, Events: []event.Event{{Key: "seen", Kind: event.KindAlert}}},
			check: func(t *testing.T, h *harness, stored []Stored) {
				t.Helper()

				require.False(t, stored[0].Duplicate)
				require.Len(t, h.events(t), 2)
			},
		},
		{
			name: "a resolved event is received with a route_event job",
			d:    Delivery{Intake: "alerts", ReceivedAt: at, Events: []event.Event{{Key: "r", Kind: event.KindResolved}}},
			check: func(t *testing.T, h *harness, stored []Stored) {
				t.Helper()

				require.Equal(t, StatusReceived, stored[0].Status)
				require.Equal(t, []string{stored[0].ID.String()}, h.routeJobs(t))
			},
		},
		{
			name:    "nothing is stored when the transaction cannot start",
			d:       Delivery{Intake: "alerts", ReceivedAt: at, Events: []event.Event{{Key: "x", Kind: event.KindAlert}}},
			cancel:  true,
			wantErr: true,
			check: func(t *testing.T, h *harness, _ []Stored) {
				t.Helper()

				require.Len(t, h.events(t), 1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			ctx := t.Context()
			if tt.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			stored, err := h.writer.Store(ctx, tt.d)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			tt.check(t, h, stored)
		})
	}
}
