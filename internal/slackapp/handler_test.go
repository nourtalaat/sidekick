package slackapp

import (
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestNew(t *testing.T) {
	t.Parallel()

	slack := &config.Slack{BotTokenSecret: registry.SecretRef{Name: "slack", Key: "botToken"}}

	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr string
	}{
		{name: "a slack section and listeners", cfg: &config.Config{Slack: slack, Listeners: map[string]config.Listener{"alerts": {Channel: "#alerts"}}}},
		{name: "no slack section", cfg: &config.Config{}, wantErr: errNoSlack.Error()},
		{name: "a listener that does not compile", cfg: &config.Config{Slack: slack, Listeners: map[string]config.Listener{"alerts": {Recovery: "("}}}, wantErr: "compile the listeners"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, err := New(Options{Config: tt.cfg, Logger: slog.New(slog.DiscardHandler), Metrics: prometheus.NewRegistry()})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Len(t, *h.listeners.Load(), len(tt.cfg.Listeners))
			require.Equal(t, AckBudget, h.ackBudget)
		})
	}
}

func TestSetListeners(t *testing.T) {
	t.Parallel()

	slack := &config.Slack{BotTokenSecret: registry.SecretRef{Name: "slack", Key: "botToken"}}

	tests := []struct {
		name      string
		listeners map[string]config.Listener
		want      []string
		wantErr   string
	}{
		{
			name:      "new listeners in name order",
			listeners: map[string]config.Listener{"ops": {Channel: "#ops"}, "billing": {Channel: "C01BILLING"}},
			want:      []string{"billing", "ops"},
		},
		{name: "no listeners", listeners: nil, want: []string{}},
		{
			name:      "a listener that does not compile",
			listeners: map[string]config.Listener{"ops": {Channel: "#ops", Recovery: "("}},
			want:      []string{"alerts"},
			wantErr:   "compile the listeners",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, err := New(Options{
				Config:  &config.Config{Slack: slack, Listeners: map[string]config.Listener{"alerts": {Channel: "#alerts"}}},
				Logger:  slog.New(slog.DiscardHandler),
				Metrics: prometheus.NewRegistry(),
			})
			require.NoError(t, err)

			err = h.SetListeners(tt.listeners)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			names := []string{}
			for _, l := range *h.listeners.Load() {
				names = append(names, l.Name)
			}

			require.Equal(t, tt.want, names)
		})
	}
}

func TestHandleWithoutStoring(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		payload     string
		wantInvalid bool
	}{
		{name: "not JSON", payload: "nope", wantInvalid: true},
		{name: "an event that does not decode", payload: `{"type": "event_callback", "event_id": "Ev1", "event": "nope"}`, wantInvalid: true},
		{name: "an event without an ID", payload: `{"type": "event_callback", "event": {"type": "message"}}`, wantInvalid: true},
		{name: "another payload type", payload: `{"type": "app_rate_limited"}`},
		{name: "another event type", payload: `{"type": "event_callback", "event_id": "Ev1", "event": {"type": "reaction_added"}}`},
		{name: "a message no listener takes", payload: `{"type": "event_callback", "event_id": "Ev1", "event": {"type": "message", "channel": "C9", "user": "U1", "text": "hi", "ts": "1.1"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, err := New(Options{
				Config:  &config.Config{Slack: &config.Slack{}, Listeners: map[string]config.Listener{"ops": {Channel: "C01OPS"}}},
				Logger:  slog.New(slog.DiscardHandler),
				Metrics: prometheus.NewRegistry(),
			})
			require.NoError(t, err)

			err = h.Handle(t.Context(), []byte(tt.payload))
			if tt.wantInvalid {
				require.ErrorIs(t, err, ErrInvalidPayload)

				return
			}

			require.NoError(t, err)
		})
	}
}
