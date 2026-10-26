package router

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestDecodePayload(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("0192d3a4-0000-7000-8000-000000000001")

	tests := []struct {
		name    string
		payload string
		want    uuid.UUID
		wantErr error
	}{
		{name: "an event ID", payload: `{"event_id": "0192d3a4-0000-7000-8000-000000000001"}`, want: id},
		{name: "no event ID", payload: `{}`, wantErr: errNoEventID},
		{name: "a nil event ID", payload: `{"event_id": "00000000-0000-0000-0000-000000000000"}`, wantErr: errNoEventID},
		{name: "not an object", payload: `[]`},
		{name: "not a UUID", payload: `{"event_id": "e1"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodePayload([]byte(tt.payload))
			if tt.want == uuid.Nil {
				require.Error(t, err)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestJobContext(t *testing.T) {
	t.Parallel()

	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

	tests := []struct {
		name        string
		traceparent *string
		wantValid   bool
	}{
		{name: "a job with a traceparent", traceparent: &traceparent, wantValid: true},
		{name: "a job without one", traceparent: nil, wantValid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sc := trace.SpanContextFromContext(jobContext(t.Context(), db.Job{Traceparent: tt.traceparent}))
			require.Equal(t, tt.wantValid, sc.IsValid())

			if tt.wantValid {
				require.True(t, sc.IsRemote())
				require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", sc.TraceID().String())
				require.Equal(t, "00f067aa0ba902b7", sc.SpanID().String())
			}
		})
	}
}

func TestTrust(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		trusted bool
		want    event.Trust
	}{
		{name: "trusted", trusted: true, want: event.Trusted},
		{name: "untrusted", trusted: false, want: event.Untrusted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, trust(db.Event{Trusted: tt.trusted}))
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	cfg, settings := loadConfig(t)

	tests := []struct {
		name    string
		opts    Options
		wantErr bool
	}{
		{name: "defaults for the logger and tracer provider", opts: Options{Config: cfg, ModelSettings: settings}},
		{name: "a config without strategies", opts: Options{Config: &config.Config{}}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := New(tt.opts)
			if tt.wantErr {
				require.ErrorIs(t, err, errNoStrategies)

				return
			}

			require.NoError(t, err)
			require.Len(t, r.routes, len(cfg.Routes))
			require.NotNil(t, r.log)
			require.NotNil(t, r.tracer)
		})
	}
}
