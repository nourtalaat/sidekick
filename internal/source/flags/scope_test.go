package flags

import (
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

// testKey is the key shape of the tests.
var testKey = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// testNow is the fixed time of the tests.
var testNow = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

func TestAllowlistCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		list    Allowlist
		wantErr []string
	}{
		{name: "one project with two environments", list: Allowlist{{Key: "shop", Environments: []string{"production", "staging"}}}},
		{name: "nothing listed", wantErr: []string{"projects: list at least one project"}},
		{
			name:    "a project without environments",
			list:    Allowlist{{Key: "shop"}},
			wantErr: []string{"projects[0].environments: list at least one environment"},
		},
		{
			name: "keys that do not fit and keys listed twice",
			list: Allowlist{
				{Key: "shop", Environments: []string{"production", "Prod*", "production"}},
				{Key: "shop", Environments: []string{"production"}},
				{Key: "proj/x", Environments: []string{"production"}},
			},
			wantErr: []string{
				`projects[0].environments[1]: "Prod*" is not an environment key`,
				`projects[0].environments[2]: "production" is listed twice`,
				`projects[1].key: "shop" is listed twice`,
				`projects[2].key: "proj/x" is not a project key`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.list.Check(testKey)
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestAllowlistScope(t *testing.T) {
	t.Parallel()

	one := Allowlist{{Key: "shop", Environments: []string{"production"}}}
	two := Allowlist{
		{Key: "shop", Environments: []string{"production", "staging"}},
		{Key: "billing", Environments: []string{"production"}},
	}

	tests := []struct {
		name        string
		list        Allowlist
		project     string
		environment string
		want        Scope
		wantErr     string
	}{
		{name: "the only project and environment may be left out", list: one, want: Scope{Project: "shop", Environment: "production"}},
		{name: "a named project and environment", list: two, project: "shop", environment: "staging",
			want: Scope{Project: "shop", Environment: "staging"}},
		{name: "the only environment of a project may be left out", list: two, project: " billing ",
			want: Scope{Project: "billing", Environment: "production"}},
		{name: "a project must be named when there are several", list: two, wantErr: "give project; allowed: shop, billing"},
		{name: "an environment must be named when there are several", list: two, project: "shop",
			wantErr: "give environment; allowed in project shop: production, staging"},
		{name: "a project outside the allowlist", list: two, project: "secret",
			wantErr: `project "secret" is not one Sidekick may read; allowed: shop, billing`},
		{name: "an environment outside the allowlist", list: two, project: "billing", environment: "staging",
			wantErr: `environment "staging" of project billing is not one Sidekick may read; allowed: production`},
		{name: "a wildcard is not an environment", list: one, environment: "*",
			wantErr: `environment "*" of project shop is not one Sidekick may read; allowed: production`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.list.Scope(tt.project, tt.environment)
			if tt.wantErr != "" {
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Equal(t, tt.wantErr, toolErr.Message)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		since   string
		until   string
		want    Window
		wantErr string
	}{
		{name: "until defaults to now", since: "2026-05-04T09:00:00Z",
			want: Window{Start: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC), End: testNow}},
		{name: "times in other zones become UTC", since: "2026-05-01T12:00:00+02:00", until: "2026-05-02T12:00:00+02:00",
			want: Window{Start: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)}},
		{name: "exactly the longest range", since: "2026-04-27T10:00:00Z",
			want: Window{Start: testNow.Add(-MaxRange), End: testNow}},
		{name: "since is required", wantErr: "give since, the start of the time range"},
		{name: "a since that is not a time", since: "yesterday", wantErr: `since: "yesterday" is not an RFC 3339 time`},
		{name: "an until that is not a time", since: "2026-05-04T09:00:00Z", until: "now", wantErr: `until: "now" is not an RFC 3339 time`},
		{name: "an empty range", since: "2026-05-04T10:00:00Z", wantErr: "since must be before until"},
		{name: "a range that is too long", since: "2026-04-27T09:59:00Z", wantErr: "the time range is 168h1m0s, longer than the 168h0m0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseWindow(tt.since, tt.until, testNow)
			if tt.wantErr != "" {
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Contains(t, toolErr.Message, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCheckKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{name: "a key", key: "new-checkout"},
		{name: "a wildcard", key: "*", wantErr: `flag: "*" is not a valid key`},
		{name: "a key that climbs out of the path", key: "../x", wantErr: `flag: "../x" is not a valid key`},
		{name: "an empty key", wantErr: `flag: "" is not a valid key`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckKey("flag", tt.key, testKey)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Equal(t, tt.wantErr, toolErr.Message)
		})
	}
}

func TestDecodeAndLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		want      ChangesInput
		wantLimit int
		wantErr   string
	}{
		{name: "no input", wantLimit: DefaultChanges},
		{name: "a limit within the bounds", input: `{"flag":"f","limit":5}`, want: ChangesInput{Flag: "f", Limit: 5}, wantLimit: 5},
		{name: "a limit above the most", input: `{"limit":1000}`, want: ChangesInput{Limit: 1000}, wantLimit: MaxChanges},
		{name: "input that does not decode", input: `{"limit":"ten"}`, wantErr: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Decode[ChangesInput](json.RawMessage(tt.input))
			if tt.wantErr != "" {
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Contains(t, toolErr.Message, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantLimit, Limit(got.Limit))
		})
	}
}
