package alertmanagersilence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	inScope := []mute.Matcher{eq("alertname", "HighLatency"), eq("service", "checkout-api")}

	extra := func(n int) []mute.Matcher {
		out := append([]mute.Matcher{}, inScope...)
		for i := range n {
			out = append(out, mute.Matcher{Name: "extra" + string(rune('a'+i)), Value: "x", IsEqual: true})
		}

		return out
	}

	tests := []struct {
		name     string
		longest  time.Duration
		matchers []mute.Matcher
		duration string
		comment  string
		raw      string
		wantErr  error
		wantText string
	}{
		{name: "a silence inside the first scope", matchers: inScope, duration: "1h", comment: "Planned work"},
		{name: "a silence inside the second scope", matchers: []mute.Matcher{eq("team", "payments")}, duration: "1h", comment: "Planned work"},
		{
			name: "extra matchers of every kind only narrow", matchers: append(append([]mute.Matcher{}, inScope...),
				mute.Matcher{Name: "env", Value: "prod|stage", IsRegex: true, IsEqual: true}, mute.Matcher{Name: "severity", Value: "page"}),
			duration: "1h", comment: "Planned work",
		},
		{name: "the most matchers", matchers: extra(mute.MaxMatchers - len(inScope)), duration: "1h", comment: "Planned work"},
		{
			name: "one matcher too many", matchers: extra(mute.MaxMatchers - len(inScope) + 1), duration: "1h", comment: "Planned work",
			wantErr: action.ErrLimit, wantText: "from 1 to 10 matchers",
		},
		{
			name: "no scope label is pinned", matchers: []mute.Matcher{eq("severity", "page")}, duration: "1h", comment: "Planned work",
			wantErr: action.ErrLimit, wantText: "scope 1 needs alertname",
		},
		{name: "one label of the scope is missing", matchers: []mute.Matcher{eq("alertname", "HighLatency")}, duration: "1h", comment: "Planned work", wantErr: action.ErrLimit},
		{
			name:     "a regex matcher on a scope label",
			matchers: []mute.Matcher{eq("alertname", "HighLatency"), {Name: "service", Value: "checkout-.*", IsRegex: true, IsEqual: true}},
			duration: "1h", comment: "Planned work", wantErr: action.ErrLimit,
		},
		{
			name:     "a not-equal matcher on a scope label",
			matchers: []mute.Matcher{eq("alertname", "HighLatency"), {Name: "service", Value: "checkout-api"}},
			duration: "1h", comment: "Planned work", wantErr: action.ErrLimit,
		},
		{
			name:     "a value outside the globs",
			matchers: []mute.Matcher{eq("alertname", "HighLatency"), eq("service", "payments-api")},
			duration: "1h", comment: "Planned work", wantErr: action.ErrLimit,
		},
		{
			name: "an empty value", matchers: []mute.Matcher{eq("team", "payments"), eq("env", "")}, duration: "1h", comment: "Planned work",
			wantErr: action.ErrLimit, wantText: "the value is empty",
		},
		{
			name: "an empty name", matchers: []mute.Matcher{eq("team", "payments"), eq("", "x")}, duration: "1h", comment: "Planned work",
			wantErr: action.ErrLimit, wantText: "the label name is empty",
		},
		{name: "no matchers", duration: "1h", comment: "Planned work", wantErr: action.ErrLimit},
		{name: "the shortest duration", matchers: inScope, duration: "5m", comment: "Planned work"},
		{name: "just below the shortest duration", matchers: inScope, duration: "4m59s", comment: "Planned work", wantErr: action.ErrLimit},
		{name: "the default longest duration", matchers: inScope, duration: "2h", comment: "Planned work"},
		{name: "just above the default longest duration", matchers: inScope, duration: "2h0m1s", comment: "Planned work", wantErr: action.ErrLimit},
		{name: "the configured longest duration", longest: 24 * time.Hour, matchers: inScope, duration: "24h", comment: "Planned work"},
		{
			name: "just above the configured longest duration", longest: 24 * time.Hour, matchers: inScope, duration: "24h0m1s", comment: "Planned work",
			wantErr: action.ErrLimit,
		},
		{name: "not a duration", matchers: inScope, duration: "an hour", comment: "Planned work", wantErr: action.ErrParams},
		{name: "the longest comment", matchers: inScope, duration: "1h", comment: strings.Repeat("a", mute.MaxCommentRunes)},
		{name: "a comment too long", matchers: inScope, duration: "1h", comment: strings.Repeat("a", mute.MaxCommentRunes+1), wantErr: action.ErrLimit},
		{name: "an empty comment", matchers: inScope, duration: "1h", comment: " ", wantErr: action.ErrLimit},
		{name: "a comment with a control character", matchers: inScope, duration: "1h", comment: "a\x1bb", wantErr: action.ErrLimit},
		{name: "a key that is not a parameter", raw: `{"matchers": [], "duration": "1h", "comment": "x", "startsAt": "2026-01-01T00:00:00Z"}`, wantErr: action.ErrParams},
		{name: "text that is not JSON", raw: `not json`, wantErr: action.ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, am := newFakeAM(t, "")
			cfg := testConfig(api.URL())
			cfg.Limits.MaxDuration = tt.longest
			s := newTestSilencer(t, api, am, cfg)

			raw := json.RawMessage(tt.raw)
			if tt.raw == "" {
				raw = paramsJSON(t, params{Matchers: tt.matchers, Duration: tt.duration, Comment: tt.comment})
			}

			err := s.Validate(t.Context(), raw)

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

	built, err := New(t.Context(), "mute-checkout", cfg, deps(api))
	require.NoError(t, err)

	spec := built.Spec()
	require.Equal(t, "mute-checkout", spec.Name)
	require.Contains(t, spec.Description, "from 5m to 6h")
	require.Contains(t, spec.Description, "scope 1 needs alertname in [HighLatency, HighErrorRate] and service in [checkout-*]")
	require.Contains(t, spec.Description, "scope 2 needs team in [payments]")
	require.Contains(t, spec.Description, "At most 10 matchers")

	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Matchers struct {
				MaxItems int `json:"maxItems"`
				Items    struct {
					Required []string `json:"required"`
				} `json:"items"`
			} `json:"matchers"`
			Duration struct {
				Description string `json:"description"`
			} `json:"duration"`
			Comment struct {
				MaxLength int `json:"maxLength"`
			} `json:"comment"`
		} `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(spec.ParamsSchema, &schema))
	require.ElementsMatch(t, []string{"matchers", "duration", "comment"}, schema.Required)
	require.Equal(t, mute.MaxMatchers, schema.Properties.Matchers.MaxItems)
	require.ElementsMatch(t, []string{"name", "value", "isRegex", "isEqual"}, schema.Properties.Matchers.Items.Required)
	require.Contains(t, schema.Properties.Duration.Description, "from 5m to 6h")
	require.Equal(t, mute.MaxCommentRunes, schema.Properties.Comment.MaxLength)
}
