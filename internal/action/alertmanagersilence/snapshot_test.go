package alertmanagersilence

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

// snapshotDoc is a snapshot's After, read back.
type snapshotDoc struct {
	Matchers  []mute.Matcher  `json:"matchers"`
	Duration  string          `json:"duration"`
	CreatedBy string          `json:"createdBy"`
	Comment   string          `json:"comment"`
	FiringNow json.RawMessage `json:"firingNow"`
}

func decodeAfter(t *testing.T, snap action.Snapshot) snapshotDoc {
	t.Helper()

	var doc snapshotDoc
	require.NoError(t, json.Unmarshal(snap.After, &doc))

	return doc
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "")
	am.setAlerts(
		fakeAlert{Labels: map[string]string{"alertname": "HighLatency", "service": "checkout-api"}, State: "active"},
		fakeAlert{Labels: map[string]string{"alertname": "HighLatency", "service": "checkout-web"}, State: "suppressed"},
	)

	s := newTestSilencer(t, api, am, testConfig(api.URL()))
	raw := paramsJSON(t, params{
		Matchers: []mute.Matcher{eq("alertname", "HighLatency"), eq("service", "checkout-api"), {Name: "env", Value: "prod|stage", IsRegex: true, IsEqual: true}},
		Duration: "90m",
		Comment:  "Planned\n database   migration",
	})

	snap, err := s.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	require.Nil(t, snap.Before, "nothing exists before the silence")
	require.Empty(t, snap.Version)
	require.Equal(t, `alertmanager:silence{alertname="HighLatency",service="checkout-api",env=~"prod|stage"}`, snap.Target)

	doc := decodeAfter(t, snap)
	require.Equal(t, "sidekick", doc.CreatedBy)
	require.Equal(t, "1h30m", doc.Duration)
	require.Equal(t, "Planned database migration", doc.Comment, "cleaned, and without the marker, which only Execute can add")
	require.Len(t, doc.Matchers, 3)

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(snap.After, &keys))
	require.NotContains(t, keys, "startsAt", "the times are set when the silence is made")
	require.NotContains(t, keys, "endsAt")

	require.JSONEq(t, `{"asOf":"2026-11-03T10:00:00Z","count":2,"first":[
		"alertname=\"HighLatency\" service=\"checkout-api\" (active)",
		"alertname=\"HighLatency\" service=\"checkout-web\" (suppressed)"]}`, string(doc.FiringNow))

	require.Equal(t, [][]string{{`alertname="HighLatency"`, `service="checkout-api"`, `env=~"prod|stage"`}}, am.filters(), "one filter for each matcher")
	require.Equal(t, []string{"GET /api/v2/alerts"}, requestPaths(api), "the snapshot reads alerts and changes nothing")
	require.Zero(t, am.postCount())

	changes, err := action.Changes(snap)
	require.NoError(t, err)
	require.Len(t, changes, 1, "an approver reads the whole new silence")
	require.Empty(t, changes[0].Path)
	require.Nil(t, changes[0].Before)
	require.NotNil(t, changes[0].After)
	require.True(t, snap.Creates())
}

func TestSnapshotPreview(t *testing.T) {
	t.Parallel()

	many := make([]fakeAlert, 0, 8)
	for i := range 8 {
		many = append(many, fakeAlert{Labels: map[string]string{"alertname": "HighLatency", "pod": strings.Repeat("p", 300) + string(rune('0'+i))}, State: "active"})
	}

	tests := []struct {
		name        string
		alerts      []fakeAlert
		alertStatus int
		wantCount   int
		wantShown   int
		wantUnknown string
	}{
		{name: "a silence that matches nothing is allowed", wantCount: 0, wantShown: 0},
		{name: "only the first few are named", alerts: many, wantCount: 8, wantShown: previewShown},
		{name: "a preview that cannot be read does not refuse the proposal", alertStatus: http.StatusServiceUnavailable, wantUnknown: "Alertmanager answered 503"},
		{name: "a refused preview does not refuse the proposal either", alertStatus: http.StatusForbidden, wantUnknown: "Alertmanager answered 403"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, am := newFakeAM(t, "")
			am.setAlerts(tt.alerts...)
			am.failWith(0, 0, tt.alertStatus)

			s := newTestSilencer(t, api, am, testConfig(api.URL()))

			snap, err := s.Snapshot(t.Context(), checkoutParams(t))
			require.NoError(t, err)

			if tt.wantUnknown != "" {
				var unknown firingUnknown
				require.NoError(t, json.Unmarshal(decodeAfter(t, snap).FiringNow, &unknown))
				require.Contains(t, unknown.Unavailable, tt.wantUnknown)
				require.LessOrEqual(t, len(unknown.Unavailable), previewMessageBytes)

				return
			}

			var seen firingNow
			require.NoError(t, json.Unmarshal(decodeAfter(t, snap).FiringNow, &seen))
			require.Equal(t, tt.wantCount, seen.Count)
			require.Len(t, seen.First, tt.wantShown)

			for _, line := range seen.First {
				require.LessOrEqual(t, len(line), previewLabelBytes+len(` (active)`), "label text of an alert is cut")
			}
		})
	}
}

func TestSnapshotRefusesWhatTheLimitsDoNotAllow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  func(*testing.T) json.RawMessage
	}{
		{name: "no scope label pinned", raw: func(t *testing.T) json.RawMessage {
			t.Helper()

			return paramsJSON(t, params{Matchers: []mute.Matcher{eq("severity", "page")}, Duration: "1h", Comment: "x"})
		}},
		{name: "a duration over the limit", raw: func(t *testing.T) json.RawMessage {
			t.Helper()

			return paramsJSON(t, params{Matchers: []mute.Matcher{eq("team", "payments")}, Duration: "3h", Comment: "x"})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, am := newFakeAM(t, "")
			s := newTestSilencer(t, api, am, testConfig(api.URL()))

			_, err := s.Snapshot(t.Context(), tt.raw(t))
			require.ErrorIs(t, err, action.ErrLimit)
			require.Empty(t, api.Requests(), "a refused proposal reads nothing")
		})
	}
}
