package datadogdowntime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// snapshotAfter is the After of a snapshot, read the way the approver sees it.
type snapshotAfter struct {
	MonitorID  int64           `json:"monitorId"`
	MonitorTag string          `json:"monitorTag"`
	Scope      string          `json:"scope"`
	Duration   string          `json:"duration"`
	Message    string          `json:"message"`
	Preview    json.RawMessage `json:"preview"`
}

func TestSnapshotOfAListedMonitor(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

	snap, err := d.Snapshot(t.Context(), monitorParams(t))
	require.NoError(t, err)

	require.Equal(t, "datadog:downtime/monitor/123", snap.Target)
	require.Nil(t, snap.Before, "nothing exists before the downtime")
	require.Empty(t, snap.Version)

	var after snapshotAfter
	require.NoError(t, json.Unmarshal(snap.After, &after))
	require.Equal(t, snapshotAfter{
		MonitorID: listedMonitor, Scope: prodScope, Duration: "1h", Message: "Planned database migration", Preview: after.Preview,
	}, after, "the duration is kept, and no start or end time")
	require.JSONEq(t, `{"name":"Checkout latency","type":"metric alert","state":"OK"}`, string(after.Preview))
	require.NotContains(t, string(snap.After), "marker")
	require.NotContains(t, string(snap.After), "sidekick proposal")

	require.Equal(t, []string{"GET /api/v1/monitor/123"}, requestPaths(api))
}

func TestSnapshotOfAMonitorAllowedByItsTag(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

	raw := paramsJSON(t, params{MonitorID: taggedMonitor, Scope: prodScope, Duration: "1h", Message: "Planned work"})

	snap, err := d.Snapshot(t.Context(), raw)
	require.NoError(t, err)
	require.Equal(t, "datadog:downtime/monitor/456", snap.Target)
}

func TestSnapshotRefusesAMonitorOutsideTheLimits(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

	raw := paramsJSON(t, params{MonitorID: unlistedMonitor, Scope: prodScope, Duration: "1h", Message: "Planned work"})

	_, err := d.Snapshot(t.Context(), raw)
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, "limits.monitorTags")
}

func TestSnapshotOfAMonitorThatDoesNotExist(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	cfg := testConfig(api.URL())
	cfg.Limits.MonitorIDs = []int64{missingMonitor}
	d := newTestDowntimer(t, api, dd, cfg)

	raw := paramsJSON(t, params{MonitorID: missingMonitor, Scope: prodScope, Duration: "1h", Message: "Planned work"})

	_, err := d.Snapshot(t.Context(), raw)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "Monitor not found")
}

func TestSnapshotRefusesParametersOutsideTheLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		p    params
	}{
		{name: "a scope that is not listed", p: params{MonitorID: listedMonitor, Scope: "env:dev", Duration: "1h", Message: "x"}},
		{name: "a duration over the limit", p: params{MonitorID: listedMonitor, Scope: prodScope, Duration: "2h1m", Message: "x"}},
		{name: "an @ in the message", p: params{MonitorID: listedMonitor, Scope: prodScope, Duration: "1h", Message: "@ana"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, dd := newFakeDD(t)
			d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

			_, err := d.Snapshot(t.Context(), paramsJSON(t, tt.p))
			require.ErrorIs(t, err, action.ErrLimit)
			require.Empty(t, api.Requests(), "a refusal needs no call")
		})
	}
}

func TestSnapshotOfATag(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

	snap, err := d.Snapshot(t.Context(), tagParams(t))
	require.NoError(t, err)
	require.Equal(t, "datadog:downtime/tag/team:payments", snap.Target)

	var after snapshotAfter
	require.NoError(t, json.Unmarshal(snap.After, &after))
	require.Equal(t, "team:payments", after.MonitorTag)
	require.Zero(t, after.MonitorID)
	require.JSONEq(t, `{"count":1,"first":["Payments errors (456)"]}`, string(after.Preview))
	require.Equal(t, []string{"team:payments"}, dd.monitorTags)
}

func TestSnapshotOfATagThatHasManyMonitors(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)

	for i := range monitorPageSize + 5 {
		dd.monitors = append(dd.monitors, fakeMonitor{ID: int64(1000 + i), Name: strings.Repeat("n", 200), Type: "query alert", Tags: []string{"team:payments"}})
	}

	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

	snap, err := d.Snapshot(t.Context(), tagParams(t))
	require.NoError(t, err)

	var after struct {
		Preview struct {
			Count   int      `json:"count"`
			AtLeast bool     `json:"atLeast"`
			First   []string `json:"first"`
		} `json:"preview"`
	}

	require.NoError(t, json.Unmarshal(snap.After, &after))
	require.Equal(t, monitorPageSize, after.Preview.Count, "the page that was read")
	require.True(t, after.Preview.AtLeast, "a full page means there may be more")
	require.Len(t, after.Preview.First, previewShown, "only the first few names")
	require.LessOrEqual(t, len(after.Preview.First[1]), previewNameBytes+20, "a long name is cut")
}

func TestSnapshotWhenThePreviewOfATagCannotBeRead(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	dd.failWith(0, 0, 0, 500)

	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

	snap, err := d.Snapshot(t.Context(), tagParams(t))
	require.NoError(t, err, "a preview that cannot be read does not refuse the proposal")

	var after snapshotAfter
	require.NoError(t, json.Unmarshal(snap.After, &after))
	require.Contains(t, string(after.Preview), "unavailable")
	require.Contains(t, string(after.Preview), "500")
}
