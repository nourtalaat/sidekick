package grafana

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestDefinitionKeepsWhatCanBeWritten(t *testing.T) {
	t.Parallel()

	var live map[string]json.RawMessage

	require.NoError(t, json.Unmarshal([]byte(`{
		"id": 7, "orgID": 1, "uid": "abc", "updated": "2026-10-01T00:00:00Z", "provenance": "",
		"title": "Checkout latency", "folderUID": "payments", "ruleGroup": "checkout",
		"notification_settings": null, "somethingNew": {"a": 1}
	}`), &live))

	got, err := definition(live)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"title": "Checkout latency", "folderUID": "payments", "ruleGroup": "checkout",
		"notification_settings": null, "somethingNew": {"a": 1}
	}`, string(got), "the read-only fields are left out and a field this code does not know is kept")
}

func TestVersionAndProvenance(t *testing.T) {
	t.Parallel()

	var live map[string]json.RawMessage

	require.NoError(t, json.Unmarshal([]byte(`{"updated": "2026-10-01T00:00:00Z", "provenance": "file"}`), &live))
	require.Equal(t, "2026-10-01T00:00:00Z", version(live))
	require.Equal(t, "file", provenance(live))

	require.Empty(t, version(map[string]json.RawMessage{}), "no updated time")
	require.Empty(t, provenance(map[string]json.RawMessage{"provenance": json.RawMessage("null")}), "a null provenance is none")
}

func TestNormalise(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "seconds come back as minutes", in: `{"for": "300s"}`, want: `{"for": "5m"}`},
		{name: "a compound duration", in: `{"for": "90s", "keep_firing_for": "3600s"}`, want: `{"for": "1m30s", "keep_firing_for": "1h"}`},
		{name: "zero", in: `{"for": "0"}`, want: `{"for": "0s"}`},
		{name: "a duration that is already in its form", in: `{"for": "5m"}`, want: `{"for": "5m"}`},
		{name: "text that is no duration stays", in: `{"for": "soon"}`, want: `{"for": "soon"}`},
		{name: "a duration that is no text stays", in: `{"for": 300}`, want: `{"for": 300}`},
		{name: "empty labels are left out", in: `{"labels": {}, "annotations": {}}`, want: `{}`},
		{name: "null labels are left out", in: `{"labels": null}`, want: `{}`},
		{name: "a null label is left out", in: `{"labels": {"team": "pay", "old": null}}`, want: `{"labels": {"team": "pay"}}`},
		{name: "a label map with only a null label is left out", in: `{"annotations": {"old": null}}`, want: `{}`},
		{name: "a label map that is no object stays", in: `{"labels": "x"}`, want: `{"labels": "x"}`},
		{
			name: "a model gets the defaults",
			in:   `{"data": [{"refId": "A", "datasourceUid": "prom", "model": {"expr": "up"}}]}`,
			want: `{"data": [{"refId": "A", "datasourceUid": "prom", "model": {"expr": "up", "refId": "A", "maxDataPoints": 43200, "intervalMs": 1000}}]}`,
		},
		{
			name: "zero and text are replaced, other numbers stay",
			in:   `{"data": [{"refId": "A", "model": {"maxDataPoints": 0, "intervalMs": "fast"}}, {"refId": "B", "model": {"maxDataPoints": 100, "intervalMs": 15000}}]}`,
			want: `{"data": [{"refId": "A", "model": {"refId": "A", "maxDataPoints": 43200, "intervalMs": 1000}}, {"refId": "B", "model": {"refId": "B", "maxDataPoints": 100, "intervalMs": 15000}}]}`,
		},
		{
			name: "the refId of the model is the refId of the query",
			in:   `{"data": [{"refId": "A", "model": {"refId": "Z", "maxDataPoints": 1, "intervalMs": 1}}]}`,
			want: `{"data": [{"refId": "A", "model": {"refId": "A", "maxDataPoints": 1, "intervalMs": 1}}]}`,
		},
		{
			name: "the query type comes from the model",
			in:   `{"data": [{"refId": "A", "queryType": "", "model": {"queryType": "range", "maxDataPoints": 1, "intervalMs": 1}}]}`,
			want: `{"data": [{"refId": "A", "queryType": "range", "model": {"queryType": "range", "refId": "A", "maxDataPoints": 1, "intervalMs": 1}}]}`,
		},
		{name: "a query with no model stays", in: `{"data": [{"refId": "A"}]}`, want: `{"data": [{"refId": "A"}]}`},
		{name: "data that is no list stays", in: `{"data": "x"}`, want: `{"data": "x"}`},
		{name: "other fields stay", in: `{"title": " Padded ", "isPaused": false}`, want: `{"title": " Padded ", "isPaused": false}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalise(json.RawMessage(tt.in))
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))

			again, err := normalise(got)
			require.NoError(t, err)
			require.JSONEq(t, string(got), string(again), "normalising twice changes nothing")
		})
	}
}

func TestNormaliseRefusesWhatIsNoObject(t *testing.T) {
	t.Parallel()

	_, err := normalise(json.RawMessage(`[1]`))
	require.Error(t, err)
}

func TestSameDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		same bool
	}{
		{name: "the same text", a: `{"for": "5m", "title": "x"}`, b: `{"title": "x", "for": "5m"}`, same: true},
		{name: "a duration in another form", a: `{"for": "300s"}`, b: `{"for": "5m"}`, same: true},
		{name: "a whole number written with a fraction", a: `{"data": [{"relativeTimeRange": {"from": 600, "to": 0}}]}`, b: `{"data": [{"relativeTimeRange": {"from": 600.0, "to": 0}}]}`, same: true},
		{name: "defaults Grafana adds to a model", a: `{"data": [{"refId": "A", "model": {"expr": "up"}}]}`, b: `{"data": [{"refId": "A", "model": {"expr": "up", "refId": "A", "maxDataPoints": 43200, "intervalMs": 1000}}]}`, same: true},
		{name: "an empty label map", a: `{"labels": {}}`, b: `{}`, same: true},
		{name: "another duration", a: `{"for": "5m"}`, b: `{"for": "10m"}`},
		{name: "another title", a: `{"title": "x"}`, b: `{"title": "y"}`},
		{name: "another expression", a: `{"data": [{"refId": "A", "model": {"expr": "up"}}]}`, b: `{"data": [{"refId": "A", "model": {"expr": "down"}}]}`},
		{name: "another label value", a: `{"labels": {"a": "1"}}`, b: `{"labels": {"a": "2"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := sameDefinition(json.RawMessage(tt.a), json.RawMessage(tt.b))
			require.NoError(t, err)
			require.Equal(t, tt.same, got)
		})
	}
}

func TestHoldsAfterAndBefore(t *testing.T) {
	t.Parallel()

	snap := action.Snapshot{
		Before: json.RawMessage(`{"title": "x", "for": "5m", "labels": {"team": "pay"}}`),
		After:  json.RawMessage(`{"title": "x", "for": "600s", "labels": {"team": "pay", "tier": "1"}}`),
	}

	tests := []struct {
		name       string
		live       string
		wantAfter  bool
		wantBefore bool
	}{
		{name: "the rule as it was", live: `{"title": "x", "for": "5m", "labels": {"team": "pay"}}`, wantBefore: true},
		{name: "the change in place, as Grafana reads it back", live: `{"title": "x", "for": "10m", "labels": {"team": "pay", "tier": "1"}}`, wantAfter: true},
		{name: "the change in place and an edit of a field it does not touch", live: `{"title": "renamed", "for": "10m", "labels": {"team": "pay", "tier": "1"}}`, wantAfter: true},
		{name: "one field changed to another value", live: `{"title": "x", "for": "15m", "labels": {"team": "pay", "tier": "1"}}`},
		{name: "half the change", live: `{"title": "x", "for": "10m", "labels": {"team": "pay"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			after, err := holdsAfter(json.RawMessage(tt.live), snap)
			require.NoError(t, err)
			require.Equal(t, tt.wantAfter, after, "after")

			before, err := holdsBefore(json.RawMessage(tt.live), snap)
			require.NoError(t, err)
			require.Equal(t, tt.wantBefore, before, "before")
		})
	}
}
