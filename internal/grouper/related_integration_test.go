//go:build integration

package grouper

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/source"
)

// relatedWindow is the related window of the tests' route.
const relatedWindow = 10 * time.Minute

func TestGroupRelated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// prepare changes the seeded rows first.
		prepare string
		event   uuid.UUID
		// relatedWindow is the route's related window; zero means
		// relatedWindow.
		relatedWindow time.Duration
		// noRelated turns related grouping off for the route.
		noRelated bool
		// noCatalog leaves the grouper without a catalog.
		noCatalog bool
		// wantJoin is a join of the seeded group; otherwise the event
		// opens a group of its own.
		wantJoin bool
	}{
		{name: "an alert of a dependent service joins the open group", event: checkoutRouted, wantJoin: true},
		{name: "another alert of the same service joins the open group", event: dbConnectionsRouted, wantJoin: true},
		{name: "the same service joins without a catalog", event: dbConnectionsRouted, noCatalog: true, wantJoin: true},
		{name: "a dependent service needs the catalog", event: checkoutRouted, noCatalog: true},
		{name: "an unrelated service opens a group", event: searchRouted},
		{name: "an event without a service opens a group", event: noServiceRouted},
		{name: "a route without a related window opens a group", event: checkoutRouted, noRelated: true},
		{name: "a group whose newest event is too old is not joined", event: checkoutRouted, relatedWindow: 4 * time.Minute},
		{
			name:    "a group of another route is not joined",
			prepare: "UPDATE event_groups SET route = 'payments'",
			event:   checkoutRouted,
		},
		{
			name:    "a group whose window ended is not joined",
			prepare: "UPDATE event_groups SET window_until = '2026-10-26 09:04:00+00'",
			event:   checkoutRouted,
		},
		{
			name:    "a closed group is not joined",
			prepare: "UPDATE event_groups SET status = 'closed', closed_reason = 'resolved'",
			event:   dbConnectionsRouted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var catalog *Catalog

			if !tt.noCatalog {
				sc := source.NewMockServiceCatalog(gomock.NewController(t))
				sc.EXPECT().ServiceDependencies(gomock.Any()).DoAndReturn(func(context.Context) ([]source.Dependency, error) {
					return []source.Dependency{{Service: "checkout-api", DependsOn: "orders-db"}}, nil
				})

				catalog = NewCatalog(map[string]source.Source{"backstage": catalogSource{MockServiceCatalog: sc}}, nil)
				catalog.Refresh(t.Context())
			}

			h := setupRelated(t, catalog)

			if tt.prepare != "" {
				_, err := h.pool.Exec(t.Context(), tt.prepare)
				require.NoError(t, err)
			}

			rt := Route{Name: "default", Window: window, RelatedWindow: relatedWindow}

			switch {
			case tt.noRelated:
				rt.RelatedWindow = 0
			case tt.relatedWindow != 0:
				rt.RelatedWindow = tt.relatedWindow
			}

			res, err := h.group(t, tt.event, rt)
			require.NoError(t, err)

			seeded := h.groupByID(t, dbGroup)
			received := time.Date(2026, 10, 26, 9, 5, 0, 0, time.UTC)
			inbox := "SELECT count(*) FROM investigation_inbox WHERE investigation_id = $1 AND kind = 'new_event' AND body->>'event_id' = $2"

			if tt.wantJoin {
				require.Equal(t, Result{GroupID: dbGroup, InvestigationID: dbInvestigation}, res)
				require.Equal(t, int32(2), seeded.EventCount)
				require.Equal(t, "fp-db-latency", seeded.Fingerprint, "the group keeps the fingerprint of the alert that opened it")
				require.True(t, received.Equal(seeded.LastEventAt))
				require.True(t, received.Add(rt.Window).Equal(*seeded.WindowUntil))
				require.Equal(t, 1, h.count(t, inbox, dbInvestigation, tt.event.String()), "the running investigation is told about the event")
				require.Equal(t, 4, h.count(t, "SELECT seq FROM publish_states WHERE investigation_id = $1", dbInvestigation), "the join refreshes the outputs")
			} else {
				require.True(t, res.Opened)
				require.NotEqual(t, dbGroup, res.GroupID)
				require.Equal(t, int32(1), seeded.EventCount)
				require.Zero(t, h.count(t, inbox, dbInvestigation, tt.event.String()))
			}

			require.Equal(t, 1, h.count(t, "SELECT count(*) FROM events WHERE id = $1 AND status = 'grouped' AND group_id = $2", tt.event, res.GroupID))
		})
	}
}

func TestGroupRelatedThenFingerprint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "an open group with the event's own fingerprint wins over a related one"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setupRelated(t, nil)

			// A second open group, with the fingerprint of the
			// db-connections alert, of the same service.
			_, err := h.pool.Exec(t.Context(), `INSERT INTO event_groups
				(id, fingerprint, route, service, status, event_count, opened_at, last_event_at, window_until)
				VALUES ('0192d3a4-0000-7000-8000-0000000001b2', 'fp-db-connections', 'default', 'orders-db', 'open', 1,
				        '2026-10-26 08:50:00+00', '2026-10-26 08:50:00+00', '2026-10-26 09:20:00+00')`)
			require.NoError(t, err)

			res, err := h.group(t, dbConnectionsRouted, Route{Name: "default", Window: window, RelatedWindow: relatedWindow})
			require.NoError(t, err)
			require.Equal(t, uuid.MustParse("0192d3a4-0000-7000-8000-0000000001b2"), res.GroupID)
			require.False(t, res.Opened)
			require.Equal(t, int32(1), h.groupByID(t, dbGroup).EventCount)
		})
	}
}
