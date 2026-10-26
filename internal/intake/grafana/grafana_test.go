package grafana

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var receivedAt = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

func TestParse(t *testing.T) {
	t.Parallel()

	memory := map[string]string{"alertname": "High memory usage", "team": "blue", "zone": "us-1"}
	cpu := map[string]string{"alertname": "High CPU usage", "team": "blue", "zone": "eu-1"}
	checkout := map[string]string{"alertname": "Checkout error rate", "grafana_folder": "Checkout", "service": "checkout", "severity": "critical"}
	play := event.Sender{Kind: event.SenderSystem, ID: "https://play.grafana.org/", Name: "My Super Webhook"}

	tests := []struct {
		name    string
		file    string
		body    string
		want    []event.Event
		wantMsg string
	}{
		{
			name: "a recorded delivery with two firing alerts",
			file: "fixture/two-firing.json",
			want: []event.Event{
				{
					Key:         wantKey("{}:{}", "c6eadffa33fcdf37", "firing", "2021-10-12T07:51:03.157076Z"),
					Kind:        event.KindAlert,
					Sender:      play,
					AlertName:   "High memory usage",
					Title:       "This alert was triggered for zone us-1",
					Text:        "The system has high memory usage",
					Severity:    event.SeverityUnknown,
					Labels:      memory,
					Fingerprint: event.Fingerprint(Adapter, "High memory usage", memory, slices.Collect(maps.Keys(memory))),
					StartsAt:    time.Date(2021, 10, 12, 7, 51, 3, 157_076_000, time.UTC),
					ReceivedAt:  receivedAt,
					Links: []event.Link{
						{Title: "Source", URL: "https://play.grafana.org/alerting/1afz29v7z/edit"},
						{Title: "Runbook", URL: "https://myrunbook.com/runbook/1234"},
					},
				},
				{
					Key:         wantKey("{}:{}", "bc97ff14869b13e3", "firing", "2021-10-12T07:56:03.157076Z"),
					Kind:        event.KindAlert,
					Sender:      play,
					AlertName:   "High CPU usage",
					Title:       "This alert was triggered for zone eu-1",
					Text:        "The system has high CPU usage",
					Severity:    event.SeverityUnknown,
					Labels:      cpu,
					Fingerprint: event.Fingerprint(Adapter, "High CPU usage", cpu, slices.Collect(maps.Keys(cpu))),
					StartsAt:    time.Date(2021, 10, 12, 7, 56, 3, 157_076_000, time.UTC),
					ReceivedAt:  receivedAt,
					Links: []event.Link{
						{Title: "Source", URL: "https://play.grafana.org/alerting/d1rdpdv7k/edit"},
						{Title: "Runbook", URL: "https://myrunbook.com/runbook/1234"},
					},
				},
			},
		},
		{
			name: "a recorded resolved alert with a dashboard, a panel and an image",
			file: "fixture/resolved.json",
			want: []event.Event{{
				Key:         wantKey(`{}/{}:{alertname="Checkout error rate", grafana_folder="Checkout"}`, "6f1d2a9b3c4e5f70", "resolved", "2026-10-26T08:50:00Z"),
				Kind:        event.KindResolved,
				Sender:      event.Sender{Kind: event.SenderSystem, ID: "https://grafana.example.com/", Name: "sidekick"},
				AlertName:   "Checkout error rate",
				Title:       "Checkout error rate above 5%",
				Text:        "The 5xx rate of checkout is back under 5%.",
				Service:     "checkout",
				Severity:    event.SeverityCritical,
				Labels:      checkout,
				Fingerprint: event.Fingerprint(Adapter, "Checkout error rate", checkout, slices.Collect(maps.Keys(checkout))),
				StartsAt:    time.Date(2026, 10, 26, 8, 50, 0, 0, time.UTC),
				EndsAt:      time.Date(2026, 10, 26, 9, 10, 0, 0, time.UTC),
				ReceivedAt:  receivedAt,
				Links: []event.Link{
					{Title: "Source", URL: "https://grafana.example.com/alerting/grafana/bdw2x8f1/view?orgId=1"},
					{Title: "Dashboard", URL: "https://grafana.example.com/d/checkout-overview?orgId=1"},
					{Title: "Panel", URL: "https://grafana.example.com/d/checkout-overview?orgId=1&viewPanel=4"},
					{Title: "Image", URL: "https://grafana.example.com/public/img/attachments/9c1e2b.png"},
				},
			}},
		},
		{
			name: "an alert without labels, annotations or times",
			body: `{"version": "1", "groupKey": "g", "alerts": [{"status": "firing", "fingerprint": "f"}]}`,
			want: []event.Event{{
				Key:         wantKey("g", "f", "firing", "0001-01-01T00:00:00Z"),
				Kind:        event.KindAlert,
				Sender:      event.Sender{Kind: event.SenderSystem},
				Severity:    event.SeverityUnknown,
				Labels:      map[string]string{},
				Fingerprint: event.Fingerprint(Adapter, "", nil, nil),
				ReceivedAt:  receivedAt,
			}},
		},
		{name: "no alerts gives no events", body: `{"version": "1", "alerts": []}`, want: []event.Event{}},
		{name: "not JSON", body: `version: 1`, wantMsg: "decode the Grafana payload"},
		{name: "not an object", body: `[]`, wantMsg: "decode the Grafana payload"},
		{name: "Alertmanager's version", body: `{"version": "4", "alerts": []}`, wantMsg: `version: only version "1" is supported`},
		{name: "a custom payload", body: `{"alert_name": "A", "status": "firing"}`, wantMsg: "leave the contact point's custom payload empty"},
		{name: "no alerts member", body: `{"version": "1"}`, wantMsg: "alerts: required"},
		{name: "alert without fingerprint", body: `{"version": "1", "alerts": [{"status": "firing"}]}`, wantMsg: "alerts[0]: fingerprint: required"},
		{name: "unknown status", body: `{"version": "1", "alerts": [{"status": "firing", "fingerprint": "a"}, {"status": "pending", "fingerprint": "b"}]}`, wantMsg: `alerts[1]: status: must be "firing" or "resolved"`},
		{name: "label of the wrong type", body: `{"version": "1", "alerts": [{"status": "firing", "fingerprint": "a", "labels": {"x": 1}}]}`, wantMsg: "alerts[0]: decode alert"},
		{name: "bad start time", body: `{"version": "1", "alerts": [{"status": "firing", "fingerprint": "a", "startsAt": "yesterday"}]}`, wantMsg: "alerts[0]: startsAt: must be an RFC 3339 time"},
		{name: "bad end time", body: `{"version": "1", "alerts": [{"status": "resolved", "fingerprint": "a", "endsAt": "yesterday"}]}`, wantMsg: "alerts[0]: endsAt: must be an RFC 3339 time"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(tt.body)

			if tt.file != "" {
				var err error

				body, err = os.ReadFile(tt.file)
				require.NoError(t, err)
			}

			a, err := New(t.Context(), "grafana", Config{}, registry.Deps{})
			require.NoError(t, err)

			events, err := a.Parse(t.Context(), intake.Request{Endpoint: "grafana", Body: body, ReceivedAt: receivedAt})
			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)
				require.NotContains(t, err.Error(), "yesterday", "errors never hold payload values")

				return
			}

			require.NoError(t, err)
			require.Len(t, events, len(tt.want))

			for i := range events {
				requireRawHoldsOnlyAlert(t, body, i, events[i].Raw)

				events[i].Raw = nil
			}

			require.Equal(t, tt.want, events)
		})
	}
}

func TestKeys(t *testing.T) {
	t.Parallel()

	const firing = `{"version": "1", "groupKey": "g", "alerts": [{"status": "firing", "fingerprint": "f", "startsAt": "2026-10-26T09:00:00Z"}]}`

	tests := []struct {
		name     string
		other    string
		wantSame bool
	}{
		{name: "a repeated notification has the same key", other: firing, wantSame: true},
		{name: "other members do not change the key", other: strings.Replace(firing, `"groupKey"`, `"receiver": "other", "groupKey"`, 1), wantSame: true},
		{name: "the same start in another zone has the same key", other: strings.Replace(firing, "09:00:00Z", "11:00:00+02:00", 1), wantSame: true},
		{name: "resolving changes the key", other: strings.Replace(firing, `"firing"`, `"resolved"`, 1)},
		{name: "firing again later changes the key", other: strings.Replace(firing, "09:00:00", "10:00:00", 1)},
		{name: "another fingerprint changes the key", other: strings.Replace(firing, `"f"`, `"f2"`, 1)},
		{name: "another group changes the key", other: strings.Replace(firing, `"g"`, `"g2"`, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base, err := Grafana{}.Parse(t.Context(), intake.Request{Body: []byte(firing)})
			require.NoError(t, err)

			other, err := Grafana{}.Parse(t.Context(), intake.Request{Body: []byte(tt.other)})
			require.NoError(t, err)

			if tt.wantSame {
				require.Equal(t, base[0].Key, other[0].Key)
			} else {
				require.NotEqual(t, base[0].Key, other[0].Key)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantMsg string
	}{
		{name: "no signature section", cfg: Config{}},
		{name: "an empty signature section", cfg: Config{Signature: &Signature{}}},
		{name: "every setting", cfg: Config{Signature: &Signature{Header: "X-Sig", TimestampHeader: "X-Ts", Tolerance: time.Minute}}},
		{name: "a header with a space", cfg: Config{Signature: &Signature{Header: "X Sig"}}, wantMsg: "signature.header: must be a header name"},
		{name: "a timestamp header with a colon", cfg: Config{Signature: &Signature{TimestampHeader: "X-Ts:"}}, wantMsg: "signature.timestampHeader: must be a header name"},
		{name: "a negative tolerance", cfg: Config{Signature: &Signature{Tolerance: -time.Second}}, wantMsg: "signature.tolerance: must not be negative"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if tt.wantMsg == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantMsg)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		cfg          Config
		wantVerifier bool
	}{
		{name: "without a signature section, the bearer token authenticates", cfg: Config{}},
		{name: "with a signature section, the adapter checks the signature", cfg: Config{Signature: &Signature{}}, wantVerifier: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, err := New(t.Context(), "grafana", tt.cfg, registry.Deps{})
			require.NoError(t, err)

			_, isVerifier := a.(intake.SignatureVerifier)
			require.Equal(t, tt.wantVerifier, isVerifier)
		})
	}
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	secret := []byte("grafana-hmac-secret")
	body := []byte(`{"version":"1","alerts":[]}`)
	now := strconv.FormatInt(receivedAt.Unix(), 10)
	old := strconv.FormatInt(receivedAt.Add(-6*time.Minute).Unix(), 10)

	tests := []struct {
		name    string
		cfg     Signature
		header  http.Header
		body    []byte
		wantMsg string
	}{
		{
			name:   "a body signature in the default header",
			header: http.Header{"X-Grafana-Alerting-Signature": {sign(secret, "", body)}},
			body:   body,
		},
		{
			name:   "a timestamped signature in custom headers",
			cfg:    Signature{Header: "X-Sig", TimestampHeader: "X-Grafana-Alerting-Timestamp"},
			header: http.Header{"X-Sig": {sign(secret, now, body)}, "X-Grafana-Alerting-Timestamp": {now}},
			body:   body,
		},
		{
			name:   "a timestamp just inside a longer tolerance",
			cfg:    Signature{TimestampHeader: "X-Ts", Tolerance: 10 * time.Minute},
			header: http.Header{"X-Grafana-Alerting-Signature": {sign(secret, old, body)}, "X-Ts": {old}},
			body:   body,
		},
		{name: "no signature", header: http.Header{}, body: body, wantMsg: "missing signature"},
		{name: "a signature that is not hex", header: http.Header{"X-Grafana-Alerting-Signature": {"zz"}}, body: body, wantMsg: "decode signature"},
		{
			name:    "a wrong secret",
			header:  http.Header{"X-Grafana-Alerting-Signature": {sign([]byte("other"), "", body)}},
			body:    body,
			wantMsg: "wrong signature",
		},
		{
			name:    "a signature of another body",
			header:  http.Header{"X-Grafana-Alerting-Signature": {sign(secret, "", body)}},
			body:    []byte(`{}`),
			wantMsg: "wrong signature",
		},
		{
			name:    "a timestamp the signature does not cover",
			cfg:     Signature{TimestampHeader: "X-Ts"},
			header:  http.Header{"X-Grafana-Alerting-Signature": {sign(secret, "", body)}, "X-Ts": {now}},
			body:    body,
			wantMsg: "wrong signature",
		},
		{
			name:    "a missing timestamp",
			cfg:     Signature{TimestampHeader: "X-Ts"},
			header:  http.Header{"X-Grafana-Alerting-Signature": {sign(secret, now, body)}},
			body:    body,
			wantMsg: "missing or unreadable timestamp",
		},
		{
			name:    "a timestamp outside the default tolerance",
			cfg:     Signature{TimestampHeader: "X-Ts"},
			header:  http.Header{"X-Grafana-Alerting-Signature": {sign(secret, old, body)}, "X-Ts": {old}},
			body:    body,
			wantMsg: "timestamp outside tolerance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, err := New(t.Context(), "grafana", Config{Signature: &tt.cfg}, registry.Deps{})
			require.NoError(t, err)

			verifier, ok := a.(intake.SignatureVerifier)
			require.True(t, ok)

			err = verifier.VerifySignature(intake.Request{Header: tt.header, Body: tt.body, ReceivedAt: receivedAt}, secret)
			if tt.wantMsg == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantMsg)
			require.ErrorIs(t, err, intake.ErrUnauthenticated)
		})
	}
}

// requireRawHoldsOnlyAlert checks that raw is the delivery with only alert
// i in alerts.
func requireRawHoldsOnlyAlert(t *testing.T, body []byte, i int, raw json.RawMessage) {
	t.Helper()

	var want, got map[string]any
	require.NoError(t, json.Unmarshal(body, &want))
	require.NoError(t, json.Unmarshal(raw, &got))

	want["alerts"] = []any{want["alerts"].([]any)[i]}
	require.Equal(t, want, got)
}

// sign signs body as Grafana does, with the timestamp when it is set.
func sign(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	if timestamp != "" {
		mac.Write([]byte(timestamp + ":"))
	}

	mac.Write(body)

	return hex.EncodeToString(mac.Sum(nil))
}

func wantKey(groupKey, fingerprint, status, startsAt string) string {
	sum := sha256.Sum256([]byte(groupKey + "\x00" + fingerprint + "\x00" + status + "\x00" + startsAt + "\x00"))

	return hex.EncodeToString(sum[:])
}
