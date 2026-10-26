package source

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSettingsCheck(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", MaxToolNameLen)

	tests := []struct {
		name     string
		source   string
		settings Settings
		want     []string
	}{
		{name: "empty settings are valid", source: "signoz"},
		{
			name:   "valid globs, rename and limits",
			source: "signoz",
			settings: Settings{
				Tools: ToolSettings{
					Filter: Filter{Allow: []string{"search_*"}, Deny: []string{"*delete*"}},
					Rename: map[string]string{"search_logs": "logs"},
				},
				Timeout:     time.Minute,
				Concurrency: 2,
			},
		},
		{
			name:     "bad allow and deny globs",
			source:   "signoz",
			settings: Settings{Tools: ToolSettings{Filter: Filter{Allow: []string{"ok", "a["}, Deny: []string{"b["}}}},
			want: []string{
				`tools.allow[1]: invalid glob "a[": syntax error in pattern`,
				`tools.deny[0]: invalid glob "b[": syntax error in pattern`,
			},
		},
		{
			name:     "renamed name too long",
			source:   "signoz",
			settings: Settings{Tools: ToolSettings{Rename: map[string]string{"search": long}}},
			want:     []string{`tools.rename.search: tool "signoz__` + long + `" is longer than 64 characters: invalid name`},
		},
		{
			name:     "renamed name with characters providers refuse",
			source:   "signoz",
			settings: Settings{Tools: ToolSettings{Rename: map[string]string{"search": "search logs"}}},
			want:     []string{`tools.rename.search: tool "search logs" of source "signoz": invalid name`},
		},
		{
			name:     "two tools renamed to one name",
			source:   "signoz",
			settings: Settings{Tools: ToolSettings{Rename: map[string]string{"a": "logs", "b": "logs"}}},
			want:     []string{`tools.rename.b: "logs" is also the new name of "a"`},
		},
		{
			name:     "an invalid source name leaves renames unchecked",
			source:   "Bad Name",
			settings: Settings{Tools: ToolSettings{Rename: map[string]string{"search": "logs"}}},
		},
		{
			name:     "negative limits",
			source:   "signoz",
			settings: Settings{Timeout: -time.Second, Concurrency: -1},
			want:     []string{"timeout: must not be negative", "concurrency: must not be negative"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.settings.Check(tt.source)
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, strings.Join(tt.want, "\n"))
		})
	}
}

func TestSettingsDefaults(t *testing.T) {
	t.Parallel()

	off := false
	on := true

	tests := []struct {
		name            string
		settings        Settings
		wantTimeout     time.Duration
		wantConcurrency int
		wantTrust       bool
	}{
		{name: "zero values use the defaults", wantTimeout: DefaultTimeout, wantConcurrency: DefaultConcurrency, wantTrust: true},
		{
			name:            "set values win",
			settings:        Settings{Timeout: time.Second, Concurrency: 1, Tools: ToolSettings{TrustAnnotations: &off}},
			wantTimeout:     time.Second,
			wantConcurrency: 1,
			wantTrust:       false,
		},
		{
			name:            "trust set to true",
			settings:        Settings{Tools: ToolSettings{TrustAnnotations: &on}},
			wantTimeout:     DefaultTimeout,
			wantConcurrency: DefaultConcurrency,
			wantTrust:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantTimeout, tt.settings.CallTimeout())
			require.Equal(t, tt.wantConcurrency, tt.settings.MaxConcurrency())
			require.Equal(t, tt.wantTrust, tt.settings.Tools.TrustsAnnotations())
			require.Equal(t, tt.settings, tt.settings.SourceSettings())
		})
	}
}

func TestToolError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "message", err: &ToolError{Message: "bad query"}, want: "tool reported an error: bad query"},
		{name: "wrapped", err: errors.Join(&ToolError{Message: "x"}), want: "tool reported an error: x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var terr *ToolError
			require.ErrorAs(t, tt.err, &terr)
			require.Equal(t, tt.want, tt.err.Error())
		})
	}
}
