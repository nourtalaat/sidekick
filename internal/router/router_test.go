package router

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

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

	cfg, in := loadConfig(t)

	tests := []struct {
		name    string
		opts    Options
		wantErr bool
	}{
		{name: "defaults for the logger and tracer provider", opts: Options{Config: cfg, ModelSettings: in.ModelSettings}},
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
			require.Len(t, *r.routes.Load(), len(cfg.Routes))
			require.NotNil(t, r.log)
			require.NotNil(t, r.tracer)
		})
	}
}

func TestSetRoutes(t *testing.T) {
	t.Parallel()

	cfg, in := loadConfig(t)

	runtime := config.NewRoute("runtime-first")
	runtime.Priority = 1
	runtime.Match = &config.Match{Service: "search-*"}

	unknown := config.NewRoute("bad-strategy")
	unknown.Strategy = "nope"

	tests := []struct {
		name    string
		ordered []config.Route
		wantErr bool
	}{
		{name: "a runtime route first", ordered: append([]config.Route{runtime}, cfg.RoutesInOrder()...)},
		{name: "a route whose strategy is not loaded", ordered: []config.Route{unknown}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := New(Options{Config: cfg, ModelSettings: in.ModelSettings})
			require.NoError(t, err)

			before := routeNames(*r.routes.Load())

			err = r.SetRoutes(tt.ordered)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, before, routeNames(*r.routes.Load()))

				return
			}

			require.NoError(t, err)

			want := make([]string, len(tt.ordered))
			for i, rc := range tt.ordered {
				want[i] = rc.Name
			}

			require.Equal(t, want, routeNames(*r.routes.Load()))
		})
	}
}

func routeNames(routes []route) []string {
	names := make([]string, len(routes))
	for i, r := range routes {
		names[i] = r.name
	}

	return names
}
