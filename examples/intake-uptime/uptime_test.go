package uptime

import (
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestParse(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		body        string
		wantKind    event.Kind
		wantKey     string
		wantTitle   string
		wantService string
		wantErr     error
	}{
		{
			name:        "a check that goes down is an alert",
			body:        `{"check_id": "api", "name": "API", "state": "down", "since": "2026-10-26T09:00:00Z", "url": "https://api.example.com/health"}`,
			wantKind:    event.KindAlert,
			wantKey:     "api:down:2026-10-26T09:00:00Z",
			wantTitle:   "API is down",
			wantService: "default-service",
		},
		{
			name:        "a check that comes back up is resolved",
			body:        `{"check_id": "api", "state": "up", "since": "2026-10-26T09:00:00Z", "service": "api"}`,
			wantKind:    event.KindResolved,
			wantKey:     "api:up:2026-10-26T09:00:00Z",
			wantTitle:   "api is up",
			wantService: "api",
		},
		{name: "no check ID", body: `{"state": "down", "since": "2026-10-26T09:00:00Z"}`, wantErr: errNoCheckID},
		{name: "no start time", body: `{"check_id": "api", "state": "down"}`, wantErr: errNoSince},
		{name: "unknown state", body: `{"check_id": "api", "state": "flapping", "since": "2026-10-26T09:00:00Z"}`, wantErr: errBadState},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, err := New(t.Context(), "uptime", Config{Service: "default-service"}, registry.Deps{})
			require.NoError(t, err)

			events, err := a.Parse(t.Context(), intake.Request{Body: []byte(tt.body)})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Len(t, events, 1)
			require.Equal(t, tt.wantKind, events[0].Kind)
			require.Equal(t, tt.wantKey, events[0].Key)
			require.Equal(t, tt.wantTitle, events[0].Title)
			require.Equal(t, tt.wantService, events[0].Service)
			require.Equal(t, since, events[0].StartsAt)
			require.JSONEq(t, tt.body, string(events[0].Raw))
		})
	}
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	secret := []byte("uptime-secret")
	body := []byte(`{"check_id": "api"}`)

	tests := []struct {
		name      string
		signature string
		wantErr   bool
	}{
		{name: "right signature", signature: hex.EncodeToString(Sign(secret, body))},
		{name: "no signature", wantErr: true},
		{name: "not hex", signature: "zz", wantErr: true},
		{name: "signed with another secret", signature: hex.EncodeToString(Sign([]byte("other"), body)), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := intake.Request{Header: http.Header{}, Body: body}
			if tt.signature != "" {
				req.Header.Set(SignatureHeader, tt.signature)
			}

			err := (&Checker{}).VerifySignature(req, secret)
			if tt.wantErr {
				require.ErrorIs(t, err, intake.ErrUnauthenticated)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{name: "no service", cfg: Config{}},
		{name: "a service", cfg: Config{Service: "api"}},
		{name: "a service with spaces around it", cfg: Config{Service: " api"}, wantErr: errServiceSpaces},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tt.cfg.Validate(), tt.wantErr)
		})
	}
}
