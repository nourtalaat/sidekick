package publish

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// testInvestigation is the investigation ID in the unit tests.
var testInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")

func TestUniqueKey(t *testing.T) {
	t.Parallel()

	require.Equal(t, "publish:0192d3a4-0000-7000-8000-0000000000c1:slack-thread", UniqueKey(testInvestigation, "slack-thread"))
}

func TestEnqueueRejectsBadUpdates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		update  Update
		wantErr error
	}{
		{name: "the queued seq", update: Update{InvestigationID: testInvestigation, Phase: output.PhaseRunning}, wantErr: errSeqTooLow},
		{name: "a negative seq", update: Update{InvestigationID: testInvestigation, Phase: output.PhaseRunning, Seq: -1}, wantErr: errSeqTooLow},
		{name: "no phase", update: Update{InvestigationID: testInvestigation, Seq: 1}, wantErr: errNoPhase},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The checks run before the database is used, so no
			// connection is needed.
			require.ErrorIs(t, Enqueue(t.Context(), nil, tt.update), tt.wantErr)
		})
	}
}

func TestDecodePayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    Payload
		wantErr error
	}{
		{
			name: "a payload",
			raw:  `{"investigation_id": "0192d3a4-0000-7000-8000-0000000000c1", "sink": "slack-thread"}`,
			want: Payload{InvestigationID: testInvestigation, Sink: "slack-thread"},
		},
		{name: "no investigation", raw: `{"sink": "slack-thread"}`, wantErr: errNoInvestigation},
		{name: "no sink", raw: `{"investigation_id": "0192d3a4-0000-7000-8000-0000000000c1"}`, wantErr: errUnknownSink},
		{name: "not JSON", raw: `nope`, wantErr: errAny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodePayload([]byte(tt.raw))

			switch {
			case errors.Is(tt.wantErr, errAny):
				require.Error(t, err)
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestEventOf(t *testing.T) {
	t.Parallel()

	received := time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC)
	starts := received.Add(-time.Minute)

	tests := []struct {
		name    string
		row     db.Event
		want    event.Event
		wantErr bool
	}{
		{
			name: "every field",
			row: db.Event{
				Intake: "alertmanager", Key: "k1", Kind: "alert", Trusted: true,
				SenderKind: new("system"), SenderID: new("am"), SenderName: new("Alertmanager"),
				AlertName: new("CheckoutLatencyP99"), Title: new("p99 above 2s"), Body: new("Slow."),
				Service: new("checkout-api"), Severity: new("critical"),
				Labels:      json.RawMessage(`{"team": "payments"}`),
				Links:       []byte(`[{"title": "Runbook", "url": "https://runbooks.example.com"}]`),
				Slack:       []byte(`{"TeamID": "T1", "Channel": "C9", "TS": "1.2", "ThreadTS": "1.1", "UserID": "U1"}`),
				Fingerprint: "fp", StartsAt: &starts, ReceivedAt: received,
			},
			want: event.Event{
				Intake: "alertmanager", Key: "k1", Kind: event.KindAlert, Trust: event.Trusted,
				Sender:    event.Sender{Kind: event.SenderSystem, ID: "am", Name: "Alertmanager"},
				AlertName: "CheckoutLatencyP99", Title: "p99 above 2s", Text: "Slow.",
				Service: "checkout-api", Severity: event.SeverityCritical,
				Labels:      map[string]string{"team": "payments"},
				Links:       []event.Link{{Title: "Runbook", URL: "https://runbooks.example.com"}},
				Slack:       &event.SlackRef{TeamID: "T1", Channel: "C9", TS: "1.2", ThreadTS: "1.1", UserID: "U1"},
				Fingerprint: "fp", StartsAt: starts, ReceivedAt: received,
			},
		},
		{
			name: "only what is required",
			row:  db.Event{Intake: "generic", Key: "k2", Kind: "alert", Labels: json.RawMessage(`{}`), Fingerprint: "fp", ReceivedAt: received},
			want: event.Event{Intake: "generic", Key: "k2", Kind: event.KindAlert, Labels: map[string]string{}, Fingerprint: "fp", ReceivedAt: received},
		},
		{name: "broken labels", row: db.Event{Labels: json.RawMessage(`[`)}, wantErr: true},
		{name: "broken links", row: db.Event{Labels: json.RawMessage(`{}`), Links: []byte(`{`)}, wantErr: true},
		{name: "broken Slack reference", row: db.Event{Labels: json.RawMessage(`{}`), Slack: []byte(`[`)}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := eventOf(tt.row)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// errAny marks a test case that expects some error.
var errAny = errors.New("any error")
