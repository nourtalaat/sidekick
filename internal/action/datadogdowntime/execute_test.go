package datadogdowntime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestExecuteMakesTheDowntimeOfAMonitor(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

	_, res := snapshotAndRun(t, d, monitorParams(t))

	bodies := dd.postedBodies()
	require.Len(t, bodies, 1)

	attrs := bodyAttributes(t, bodies[0])
	require.ElementsMatch(t, []string{"message", "monitor_identifier", "schedule", "scope"}, keysOf(attrs),
		"only the downtime is sent: the preview for the approver and the duration are not")
	require.JSONEq(t, `{"monitor_id":123}`, string(attrs["monitor_identifier"]))
	require.JSONEq(t, `{"end":"2026-11-03T11:00:00+00:00"}`, string(attrs["schedule"]),
		"no start, so it starts when it is made; it ends the duration the approver read after now")
	require.JSONEq(t, `"env:prod"`, string(attrs["scope"]))

	var message string
	require.NoError(t, json.Unmarshal(attrs["message"], &message))
	require.Equal(t, messageWithMarker("Planned database migration", proposalID), message)
	require.True(t, mute.HasMarker(message, proposalID))
	require.True(t, hasMarker(message, proposalID))

	require.Equal(t, "Muted monitor 123 for 1h (downtime dt-1).", res.Summary)
	require.Empty(t, res.Links, "a test server has no app host")
	require.Equal(t, []string{"GET /api/v1/monitor/123", "GET /api/v2/downtime", "POST /api/v2/downtime"}, requestPaths(api))
}

func TestExecuteMakesTheDowntimeOfATag(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

	_, res := snapshotAndRun(t, d, tagParams(t))

	attrs := bodyAttributes(t, dd.postedBodies()[0])
	require.JSONEq(t, `{"monitor_tags":["team:payments"]}`, string(attrs["monitor_identifier"]), "exactly the one tag, never the wildcard")
	require.JSONEq(t, `"env:staging AND service:checkout"`, string(attrs["scope"]))
	require.JSONEq(t, `{"end":"2026-11-03T10:30:00+00:00"}`, string(attrs["schedule"]))
	require.Equal(t, "Muted the monitors with tag team:payments for 30m (downtime dt-1).", res.Summary)
}

func TestTheResultLinksToTheDowntimesPage(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)

	cfg := testConfig("")
	cfg.Site = "datadoghq.eu"

	built, err := New(t.Context(), "mute", cfg, deps(api))
	require.NoError(t, err)

	d, ok := built.(*downtimer)
	require.True(t, ok)
	require.Equal(t, "app.datadoghq.eu", d.appHost)
	require.Equal(t, []action.Link{{Title: "Downtime dt-1", URL: "https://app.datadoghq.eu/monitors/downtimes"}}, d.result("x", "dt-1").Links)
}

func TestExecuteStartsWhenItRuns(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := monitorParams(t)

	snap, err := d.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	dd.advance(23 * time.Hour)

	_, err = d.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err, "a proposal approved a day later still makes a downtime that lasts as long as the approver read")

	attrs := bodyAttributes(t, dd.postedBodies()[0])
	require.JSONEq(t, `{"end":"2026-11-04T10:00:00+00:00"}`, string(attrs["schedule"]))
}

func TestExecuteTwiceMakesOneDowntime(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := monitorParams(t)

	snap, first := snapshotAndRun(t, d, raw)

	again, err := d.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)

	require.Len(t, dd.postedBodies(), 1, "a retry after a crash finds the downtime by its marker")
	require.Equal(t, first.Links, again.Links)
	require.Equal(t, "Downtime dt-1 exists already.", again.Summary)
}

func TestExecuteFindsTheMarkerWhenDatadogTurnedTheLineBreakIntoASpace(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	dd.mangle = func(message string) string { return strings.ReplaceAll(message, "\n", " ") }

	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := monitorParams(t)

	snap, _ := snapshotAndRun(t, d, raw)
	require.Equal(t, "Planned database migration "+mute.Marker(proposalID), dd.get("dt-1").Message, "the fake gave the message back with a space")

	_, err := d.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)
	require.Len(t, dd.postedBodies(), 1, "a message given back with a space in place of the line break is still found")
}

func TestExecuteDoesNotMakeASecondDowntimeWhenTheFirstWasCanceled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
	}{
		{name: "a person canceled it", status: "canceled"},
		{name: "it ended", status: "ended"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, dd := newFakeDD(t)
			d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
			raw := monitorParams(t)

			snap, _ := snapshotAndRun(t, d, raw)
			dd.edit("dt-1", func(f *fakeDowntime) { f.Status = tt.status })

			_, err := d.Execute(t.Context(), invocation(raw, snap))
			require.NoError(t, err)
			require.Len(t, dd.postedBodies(), 1, "a downtime that a person or the clock ended is not made again")
		})
	}
}

func TestExecuteIgnoresTheDowntimeOfAnotherProposal(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	dd.add(fakeDowntime{MonitorID: listedMonitor, Scope: prodScope, Message: messageWithMarker("Other work", otherProposalID), Start: testNow, End: testNow.Add(time.Hour)})
	dd.add(fakeDowntime{MonitorID: listedMonitor, Scope: prodScope, Message: "Planned database migration", Start: testNow, End: testNow.Add(time.Hour)})
	dd.add(fakeDowntime{MonitorID: listedMonitor, Scope: prodScope, Message: "Planned " + mute.Marker(proposalID) + " work", Start: testNow, End: testNow.Add(time.Hour)})

	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))

	_, res := snapshotAndRun(t, d, monitorParams(t))
	require.Len(t, dd.postedBodies(), 1)
	require.Equal(t, "Muted monitor 123 for 1h (downtime dt-4).", res.Summary)
}

func TestExecuteReadsEveryPageOfTheList(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	dd.pageCap = 2

	for range 5 {
		dd.add(fakeDowntime{MonitorID: listedMonitor, Scope: prodScope, Message: "Someone else's", Start: testNow, End: testNow.Add(time.Hour)})
	}

	dd.add(fakeDowntime{MonitorID: listedMonitor, Scope: prodScope, Message: messageWithMarker("Planned database migration", proposalID), Start: testNow, End: testNow.Add(time.Hour)})

	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := monitorParams(t)

	snap, err := d.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	res, err := d.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)
	require.Equal(t, "Downtime dt-6 exists already.", res.Summary, "the marker is on the third page, which asked for 100 and got 2")
	require.Empty(t, dd.postedBodies())
	require.Equal(t, 3, dd.lists())
}

func TestExecuteChecksAMonitorAllowedOnlyByItsTagAgain(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := paramsJSON(t, params{MonitorID: taggedMonitor, Scope: prodScope, Duration: "1h", Message: "Planned work"})

	snap, err := d.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	dd.monitors[1].Tags = []string{"team:search"}

	_, err = d.Execute(t.Context(), invocation(raw, snap))
	require.ErrorIs(t, err, action.ErrLimit, "the monitor lost the tag between the proposal and the run")
	require.Empty(t, dd.postedBodies())
}

func TestExecuteChecksTheLimitsAgain(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := monitorParams(t)

	snap, err := d.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	d.limits.Scopes = []string{"env:staging"}

	_, err = d.Execute(t.Context(), invocation(raw, snap))
	require.ErrorIs(t, err, action.ErrLimit, "a scope that left the limits since the proposal")
	require.Empty(t, dd.postedBodies())
}

func TestExecuteFailures(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := monitorParams(t)

	snap, err := d.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	other := snap
	other.Target = "datadog:downtime/monitor/999"

	bad := snap
	bad.After = json.RawMessage(`[1]`)

	otherScope := snap
	otherScope.After = json.RawMessage(strings.Replace(string(snap.After), prodScope, stagingScope, 1))

	tests := []struct {
		name    string
		inv     action.Invocation
		wantErr error
	}{
		{name: "no snapshot", inv: action.Invocation{ProposalID: proposalID, Params: raw}, wantErr: errNoSnapshot},
		{name: "a snapshot of another monitor", inv: invocation(raw, other), wantErr: errTargetDiffer},
		{name: "a snapshot of another scope", inv: invocation(raw, otherScope), wantErr: errTargetDiffer},
		{name: "a snapshot that cannot be read", inv: invocation(raw, bad), wantErr: errBadSnapshot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := d.Execute(t.Context(), tt.inv)
			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Empty(t, dd.postedBodies())
		})
	}
}

func TestExecuteErrorsFromDatadog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		list      int
		post      int
		permanent bool
	}{
		{name: "the list is refused", list: 403, permanent: true},
		{name: "the list fails", list: 500},
		{name: "the post is refused", post: 400, permanent: true},
		{name: "the post fails", post: 502},
		{name: "too many requests", post: 429},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, dd := newFakeDD(t)
			d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
			raw := monitorParams(t)

			snap, err := d.Snapshot(t.Context(), raw)
			require.NoError(t, err)

			dd.failWith(tt.list, tt.post, 0, 0)

			_, err = d.Execute(t.Context(), invocation(raw, snap))
			require.Error(t, err)
			require.Equal(t, tt.permanent, errors.Is(err, action.ErrPermanent), "%v", err)
		})
	}
}

func TestExecuteGivesUpOnAListThatIsTooLong(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	dd.pageCap = 1

	for range maxListPages + 1 {
		dd.downtimes = append(dd.downtimes, &fakeDowntime{Status: "ended", Scope: "x", Message: "x"})
	}

	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := monitorParams(t)

	snap, err := d.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	_, err = d.Execute(t.Context(), invocation(raw, snap))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorIs(t, err, errTooManyDowntimes)
	require.Empty(t, dd.postedBodies(), "a downtime is never made without looking at the whole list")
}

// keysOf returns the keys of m.
func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}
