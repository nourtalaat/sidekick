package datadogdowntime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	ok := params{MonitorID: listedMonitor, Scope: prodScope, Duration: "1h", Message: "Planned work"}

	with := func(change func(*params)) params {
		p := ok
		change(&p)

		return p
	}

	tests := []struct {
		name     string
		longest  time.Duration
		noTags   bool
		p        params
		raw      string
		wantErr  error
		wantText string
	}{
		{name: "a listed monitor", p: ok},
		{name: "a monitor allowed by its tags, read later", p: with(func(p *params) { p.MonitorID = taggedMonitor })},
		{name: "a monitor by tag", p: with(func(p *params) { p.MonitorID, p.MonitorTag = 0, "team:payments" })},
		{name: "the second scope", p: with(func(p *params) { p.Scope = stagingScope })},
		{name: "a monitor that is not listed, with no tags in the limits", noTags: true, p: with(func(p *params) { p.MonitorID = taggedMonitor }), wantErr: action.ErrLimit, wantText: "limits.monitorIds"},
		{name: "a tag that is not listed", p: with(func(p *params) { p.MonitorID, p.MonitorTag = 0, "team:search" }), wantErr: action.ErrLimit, wantText: "limits.monitorTags"},
		{name: "the tag that mutes every monitor", p: with(func(p *params) { p.MonitorID, p.MonitorTag = 0, "*" }), wantErr: action.ErrLimit},
		{name: "a tag with other letters", p: with(func(p *params) { p.MonitorID, p.MonitorTag = 0, "TEAM:payments" }), wantErr: action.ErrLimit},
		{name: "both a monitor and a tag", p: with(func(p *params) { p.MonitorTag = "team:payments" }), wantErr: action.ErrParams, wantText: "exactly one"},
		{name: "neither a monitor nor a tag", p: with(func(p *params) { p.MonitorID = 0 }), wantErr: action.ErrParams, wantText: "exactly one"},
		{name: "a negative monitor ID", p: with(func(p *params) { p.MonitorID = -1 }), wantErr: action.ErrParams},
		{name: "a scope that is not listed", p: with(func(p *params) { p.Scope = "env:dev" }), wantErr: action.ErrLimit, wantText: "limits.scopes"},
		{name: "a scope that only contains a listed one", p: with(func(p *params) { p.Scope = "env:prod OR env:dev" }), wantErr: action.ErrLimit},
		{name: "the scope * when it is not listed", p: with(func(p *params) { p.Scope = "*" }), wantErr: action.ErrLimit},
		{name: "the shortest duration", p: with(func(p *params) { p.Duration = "5m" })},
		{name: "just below the shortest duration", p: with(func(p *params) { p.Duration = "4m59s" }), wantErr: action.ErrLimit},
		{name: "the default longest duration", p: with(func(p *params) { p.Duration = "2h" })},
		{name: "just above the default longest duration", p: with(func(p *params) { p.Duration = "2h0m1s" }), wantErr: action.ErrLimit, wantText: "above 2h"},
		{name: "the configured longest duration", longest: 24 * time.Hour, p: with(func(p *params) { p.Duration = "24h" })},
		{name: "just above the configured longest duration", longest: 24 * time.Hour, p: with(func(p *params) { p.Duration = "24h0m1s" }), wantErr: action.ErrLimit},
		{name: "not a duration", p: with(func(p *params) { p.Duration = "an hour" }), wantErr: action.ErrParams},
		{name: "the longest message", p: with(func(p *params) { p.Message = strings.Repeat("a", mute.MaxCommentRunes) })},
		{name: "a message too long", p: with(func(p *params) { p.Message = strings.Repeat("a", mute.MaxCommentRunes+1) }), wantErr: action.ErrLimit},
		{name: "an empty message", p: with(func(p *params) { p.Message = " " }), wantErr: action.ErrLimit},
		{name: "a message with a control character", p: with(func(p *params) { p.Message = "a\x1bb" }), wantErr: action.ErrLimit},
		{name: "a message with an @", p: with(func(p *params) { p.Message = "tell @pagerduty-oncall" }), wantErr: action.ErrLimit, wantText: "@"},
		{name: "a message with the text of a marker", p: with(func(p *params) { p.Message = "ok [sidekick proposal x]" }), wantErr: action.ErrLimit, wantText: "marker"},
		{name: "a key that is not a parameter", raw: `{"monitorId": 123, "scope": "env:prod", "duration": "1h", "message": "x", "start": "now"}`, wantErr: action.ErrParams},
		{name: "text that is not JSON", raw: `not json`, wantErr: action.ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, dd := newFakeDD(t)
			cfg := testConfig(api.URL())
			cfg.Limits.MaxDuration = tt.longest

			if tt.noTags {
				cfg.Limits.MonitorTags = nil
			}

			d := newTestDowntimer(t, api, dd, cfg)

			raw := json.RawMessage(tt.raw)
			if tt.raw == "" {
				raw = paramsJSON(t, tt.p)
			}

			err := d.Validate(t.Context(), raw)

			if tt.wantErr == nil {
				require.NoError(t, err)
				require.Empty(t, api.Requests(), "Validate makes no network call")

				return
			}

			require.ErrorIs(t, err, tt.wantErr)

			if tt.wantText != "" {
				require.ErrorContains(t, err, tt.wantText)
			}
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	cfg := testConfig(api.URL())
	cfg.Limits.MaxDuration = 6 * time.Hour

	built, err := New(t.Context(), "mute-prod", cfg, deps(api))
	require.NoError(t, err)

	spec := built.Spec()
	require.Equal(t, "mute-prod", spec.Name)
	require.Contains(t, spec.Description, "from 5m to 6h")
	require.Contains(t, spec.Description, "Allowed monitor IDs: 123")
	require.Contains(t, spec.Description, "team:payments")
	require.Contains(t, spec.Description, "env:prod | env:staging AND service:checkout")
	require.Contains(t, spec.Description, "without @ handles")

	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			MonitorID struct {
				Minimum int `json:"minimum"`
			} `json:"monitorId"`
			Duration struct {
				Description string `json:"description"`
			} `json:"duration"`
			Message struct {
				MaxLength int `json:"maxLength"`
			} `json:"message"`
		} `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(spec.ParamsSchema, &schema))
	require.ElementsMatch(t, []string{"scope", "duration", "message"}, schema.Required)
	require.Equal(t, 1, schema.Properties.MonitorID.Minimum)
	require.Contains(t, schema.Properties.Duration.Description, "from 5m to 6h")
	require.Equal(t, mute.MaxCommentRunes, schema.Properties.Message.MaxLength)
}

func TestNewNeedsSecretsAndALimitInRange(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)

	_, err := New(t.Context(), "mute", testConfig(api.URL()), registry.Deps{HTTP: api.Client()})
	require.ErrorIs(t, err, errNoSecrets)

	cfg := testConfig(api.URL())
	cfg.Limits.MaxDuration = mute.CeilingMaxDuration + time.Second

	_, err = New(t.Context(), "mute", cfg, deps(api))
	require.ErrorContains(t, err, "limits.maxDuration")
	require.Empty(t, api.Requests(), "the constructor makes no network call")
}

func TestTheActionIsRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), Type)
}
