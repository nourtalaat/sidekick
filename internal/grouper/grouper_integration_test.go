//go:build integration

package grouper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nourtalaat/sidekick/internal/event"
)

// window is the grouping window of the tests' route.
const window = 30 * time.Minute

func TestGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// prepare changes the seeded rows first.
		prepare string
		event   string
		labels  []string
		window  time.Duration
		// wantJoin is a join of the seeded group; otherwise a new group
		// opens with wantFingerprint.
		wantJoin        bool
		wantFingerprint string
		// wantSeededReason is how the seeded group ends; nil leaves it
		// open.
		wantSeededReason *string
	}{
		{name: "an event joins the open group", event: "disk", wantJoin: true},
		{name: "another fingerprint opens a group", event: "cpu", wantFingerprint: "fp-cpu"},
		{
			name:            "fingerprint labels make the fingerprint",
			event:           "cpu",
			labels:          []string{"env"},
			wantFingerprint: event.Fingerprint("grafana", "HighCPU", map[string]string{"env": "prod"}, []string{"env"}),
		},
		{
			name:             "a group whose window ended is closed",
			prepare:          "UPDATE event_groups SET window_until = '2026-10-26 09:05:00+00'",
			event:            "disk",
			wantFingerprint:  "fp-disk",
			wantSeededReason: new(ClosedWindowEnded),
		},
		{
			name:     "a group without a window joins within the route's window",
			prepare:  "UPDATE event_groups SET window_until = NULL",
			event:    "disk",
			wantJoin: true,
		},
		{
			name:             "a group without a window ends the route's window after its newest event",
			prepare:          "UPDATE event_groups SET window_until = NULL",
			event:            "disk",
			window:           5 * time.Minute,
			wantFingerprint:  "fp-disk",
			wantSeededReason: new(ClosedWindowEnded),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			if tt.prepare != "" {
				_, err := h.pool.Exec(t.Context(), tt.prepare)
				require.NoError(t, err)
			}

			id := diskRouted
			if tt.event == "cpu" {
				id = cpuRouted
			}

			rt := Route{Name: "default", Window: window, FingerprintLabels: tt.labels}
			if tt.window != 0 {
				rt.Window = tt.window
			}

			res, err := h.group(t, id, rt)
			require.NoError(t, err)

			seeded := h.groupByID(t, diskGroup)
			received := time.Date(2026, 10, 26, 9, 10, 0, 0, time.UTC)

			if tt.wantJoin {
				require.Equal(t, Result{GroupID: diskGroup, InvestigationID: diskInvestigation}, res)
				require.Equal(t, int32(2), seeded.EventCount)
				require.True(t, received.Equal(seeded.LastEventAt))
				require.True(t, received.Add(rt.Window).Equal(*seeded.WindowUntil))
				require.Equal(t, 1, h.count(t, "SELECT count(*) FROM investigation_inbox WHERE investigation_id = $1 AND kind = 'new_event'", diskInvestigation))
				require.Equal(t, 4, h.count(t, "SELECT seq FROM publish_states WHERE investigation_id = $1", diskInvestigation), "the join refreshes the outputs")
			} else {
				require.True(t, res.Opened)
				require.NotEqual(t, diskGroup, res.GroupID)

				opened := h.groupByID(t, res.GroupID)
				require.Equal(t, tt.wantFingerprint, opened.Fingerprint)
				require.Equal(t, "open", opened.Status)
				require.Equal(t, int32(1), opened.EventCount)
				require.Equal(t, &res.InvestigationID, opened.InvestigationID)
				require.True(t, received.Add(rt.Window).Equal(*opened.WindowUntil))
				require.Zero(t, h.count(t, "SELECT count(*) FROM investigation_inbox"))
			}

			require.Equal(t, tt.wantSeededReason, seeded.ClosedReason)
			require.Equal(t, 1, h.count(t, "SELECT count(*) FROM events WHERE id = $1 AND status = 'grouped' AND group_id = $2", id, res.GroupID))

			spans := h.spans.Ended()
			require.Len(t, spans, 1)
			require.Equal(t, SpanName, spans[0].Name())
			require.ElementsMatch(t, []attribute.KeyValue{
				attribute.String(attrRoute, "default"),
				attribute.String(attrGroupID, res.GroupID.String()),
			}, spans[0].Attributes())
		})
	}
}

func TestGroupFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare string
	}{
		{name: "an event that is grouped already", prepare: "UPDATE events SET status = 'grouped' WHERE id = $1"},
		{name: "an event that intake skipped", prepare: "UPDATE events SET status = 'skipped' WHERE id = $1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			_, err := h.pool.Exec(t.Context(), tt.prepare, diskRouted)
			require.NoError(t, err)

			_, err = h.group(t, diskRouted, Route{Name: "default", Window: window})
			require.ErrorIs(t, err, errNotRouted)
			require.Equal(t, int32(1), h.groupByID(t, diskGroup).EventCount, "the failed transaction wrote nothing")

			spans := h.spans.Ended()
			require.Len(t, spans, 1)
			require.Equal(t, codes.Error, spans[0].Status().Code)
		})
	}
}
