package action

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
)

func TestEffectiveMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		route               Mode
		trust               event.Trust
		allowUntrustedWrite bool
		want                Mode
	}{
		{name: "unset mode is read-only", route: "", trust: event.Trusted, want: ReadOnly},
		{name: "unset mode is read-only for untrusted events", route: "", trust: event.Untrusted, want: ReadOnly},
		{name: "read-only stays read-only", route: ReadOnly, trust: event.Trusted, want: ReadOnly},
		{name: "approved stays approved", route: Approved, trust: event.Untrusted, want: Approved},
		{name: "read-write with a trusted sender", route: ReadWrite, trust: event.Trusted, want: ReadWrite},
		{name: "read-write with an untrusted sender drops to approved", route: ReadWrite, trust: event.Untrusted, want: Approved},
		{
			name:                "read-write with an untrusted sender and the override",
			route:               ReadWrite,
			trust:               event.Untrusted,
			allowUntrustedWrite: true,
			want:                ReadWrite,
		},
		{name: "an unknown trust value counts as untrusted", route: ReadWrite, trust: "maybe", want: Approved},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, EffectiveMode(tt.route, tt.trust, tt.allowUntrustedWrite))
		})
	}
}

func TestResultJSONOfThePullRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result Result
		want   string
	}{
		{name: "no pull request", result: Result{Summary: "done"}, want: `{"summary":"done","links":null}`},
		{
			name:   "a pull request",
			result: Result{Summary: "done", PullRequest: &PullRequestRef{Number: 21, HeadSHA: "abc"}},
			want:   `{"summary":"done","links":null,"pullRequest":{"number":21,"headSHA":"abc"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(tt.result)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(raw))

			var back Result

			require.NoError(t, json.Unmarshal(raw, &back))
			require.Equal(t, tt.result, back)
		})
	}
}
