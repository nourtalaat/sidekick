package api

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	apispec "github.com/nourtalaat/sidekick/api"
	"github.com/nourtalaat/sidekick/internal/api/apigen"
)

func TestHubNotify(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	other := uuid.New()

	tests := []struct {
		name    string
		payload string
		want    uint32
	}{
		{name: "a step", payload: id.String() + ":step:7", want: changeSteps},
		{name: "a status", payload: id.String() + ":status:final", want: changeStatus},
		{name: "a watcher", payload: id.String() + ":watchers", want: changeWatchers},
		{name: "a kind this version does not know", payload: id.String() + ":comment:1", want: changeAll},
		{name: "no kind", payload: id.String(), want: changeAll},
		{name: "another investigation", payload: other.String() + ":step:1"},
		{name: "not an ID", payload: "marker"},
		{name: "empty", payload: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hub := NewHub(HubOptions{})

			sub, err := hub.Subscribe(id)
			require.NoError(t, err)

			hub.notify(tt.payload)

			require.Equal(t, tt.want, sub.Take())

			if tt.want == 0 {
				require.Empty(t, sub.Wake())
			} else {
				require.Len(t, sub.Wake(), 1)
			}
		})
	}
}

// TestSubscriptionJoinsChanges checks that changes that arrive while a
// stream is busy are joined into one wake-up with all their flags, and that
// the hub keeps nothing more per stream.
func TestSubscriptionJoinsChanges(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	hub := NewHub(HubOptions{})

	sub, err := hub.Subscribe(id)
	require.NoError(t, err)

	for range 10000 {
		hub.notify(id.String() + ":step:1")
	}

	hub.notify(id.String() + ":status:final")

	require.Len(t, sub.Wake(), 1, "the wake-up channel holds one place")
	require.Equal(t, changeSteps|changeStatus, sub.Take())
	require.Zero(t, sub.Take())
}

func TestHubLimitsStreams(t *testing.T) {
	t.Parallel()

	hub := NewHub(HubOptions{MaxStreams: 2})
	id := uuid.New()

	first, err := hub.Subscribe(id)
	require.NoError(t, err)

	_, err = hub.Subscribe(uuid.New())
	require.NoError(t, err)

	_, err = hub.Subscribe(id)
	require.ErrorIs(t, err, errTooManyStreams)

	first.Close()
	first.Close()

	third, err := hub.Subscribe(id)
	require.NoError(t, err, "a closed subscription frees its place, once however often it is closed")

	third.Close()
	require.Len(t, hub.subs, 1, "an investigation nobody watches is forgotten")
	require.Equal(t, 1, hub.count)
}

func TestHubWakeAll(t *testing.T) {
	t.Parallel()

	hub := NewHub(HubOptions{})

	a, err := hub.Subscribe(uuid.New())
	require.NoError(t, err)

	b, err := hub.Subscribe(uuid.New())
	require.NoError(t, err)

	hub.wakeAll()

	require.Equal(t, changeAll, a.Take())
	require.Equal(t, changeAll, b.Take())
}

func TestStreamStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  apigen.StreamInvestigationParams
		want    int32
		wantErr bool
	}{
		{name: "from the start", want: -1},
		{name: "after a step", params: apigen.StreamInvestigationParams{After: new(int32(4))}, want: 4},
		{name: "Last-Event-ID", params: apigen.StreamInvestigationParams{LastEventID: new("7")}, want: 7},
		{name: "Last-Event-ID wins", params: apigen.StreamInvestigationParams{After: new(int32(4)), LastEventID: new("7")}, want: 7},
		{name: "an empty Last-Event-ID is ignored", params: apigen.StreamInvestigationParams{After: new(int32(4)), LastEventID: new("")}, want: 4},
		{name: "Last-Event-ID not a number", params: apigen.StreamInvestigationParams{LastEventID: new("x")}, wantErr: true},
		{name: "Last-Event-ID too big", params: apigen.StreamInvestigationParams{LastEventID: new("99999999999")}, wantErr: true},
		{name: "Last-Event-ID below the start", params: apigen.StreamInvestigationParams{LastEventID: new("-2")}, wantErr: true},
		{name: "after below the start", params: apigen.StreamInvestigationParams{After: new(int32(-2))}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := streamStart(tt.params)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestWriteEvent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	require.NoError(t, writeEvent(&buf, "step", new(int32(3)), []byte(`{"seq":3}`)))
	require.NoError(t, writeEvent(&buf, "status", nil, []byte(`{"status":"final"}`)))

	require.Equal(t, "id: 3\nevent: step\ndata: {\"seq\":3}\n\nevent: status\ndata: {\"status\":\"final\"}\n\n", buf.String())
}

// TestStreamStatusMatchesTheSpec checks that the data of a status event
// has the fields the StreamStatus schema lists, which the generated Go
// code leaves out because no response refers to it.
func TestStreamStatusMatchesTheSpec(t *testing.T) {
	t.Parallel()

	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string       `yaml:"required"`
				Properties map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}

	require.NoError(t, yaml.Unmarshal(apispec.Spec, &doc))

	schema, ok := doc.Components.Schemas["StreamStatus"]
	require.True(t, ok)

	full, err := json.Marshal(streamStatus{
		Verdict: new("true_positive"), Confidence: new("high"), StopReason: new("submitted"),
		FinishedAt: new(time.Now()), CancelRequestedAt: new(time.Now()),
	})
	require.NoError(t, err)

	var fields map[string]any

	require.NoError(t, json.Unmarshal(full, &fields))

	got := make([]string, 0, len(fields))
	for k := range fields {
		got = append(got, k)
	}

	want := make([]string, 0, len(schema.Properties))
	for k := range schema.Properties {
		want = append(want, k)
	}

	require.ElementsMatch(t, want, got, "the fields of streamStatus and of the StreamStatus schema")

	var minimal map[string]any

	bare, err := json.Marshal(streamStatus{})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(bare, &minimal))

	for _, name := range schema.Required {
		require.Contains(t, minimal, name, "%s is required by the schema, so it is always sent", name)
	}
}
