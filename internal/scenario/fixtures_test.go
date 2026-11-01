package scenario

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/mock"
)

// errAny stands for any error in a table.
var errAny = errors.New("any error")

// call is a recorded call for the tests.
func call(seq int32, src, tool, status, input, text string) investigator.RecordedCall {
	return investigator.RecordedCall{Seq: seq, CallID: tool, Source: src, Tool: tool, Status: status, Input: json.RawMessage(input), Text: text}
}

func TestFixturesOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		calls           []investigator.RecordedCall
		want            map[string]mock.Config
		wantUnavailable []string
		wantProvenance  []Provenance
		wantErr         error
	}{
		{
			name: "a fixture with more matchers comes before one with fewer",
			calls: []investigator.RecordedCall{
				call(1, "github", "list_commits", "ok", `{}`, "all"),
				call(2, "github", "list_commits", "ok", `{"repo":"a/b"}`, "one repo"),
			},
			want: map[string]mock.Config{"github": {Fixtures: []mock.Fixture{
				{Tool: "list_commits", Match: mock.Match{Args: map[string]mock.Matcher{"repo": {Equals: "a/b"}}}, Respond: &mock.Response{Text: "one repo"}},
				{Tool: "list_commits", Respond: &mock.Response{Text: "all"}},
			}}},
			wantProvenance: []Provenance{{Source: "github", Fixture: 0, Seq: 2}, {Source: "github", Fixture: 1, Seq: 1}},
		},
		{
			name: "calls with the same match share a fixture with ordered responses",
			calls: []investigator.RecordedCall{
				call(1, "k8s", "get_pods", "ok", `{"namespace":"prod","limit":1}`, "first"),
				call(2, "k8s", "get_pods", "no_data", `{"namespace":"prod","limit":2}`, sidekickNoDataText),
			},
			want: map[string]mock.Config{"k8s": {Fixtures: []mock.Fixture{{
				Tool:      "get_pods",
				Match:     mock.Match{Args: map[string]mock.Matcher{"namespace": {Equals: "prod"}}},
				Responses: []mock.Response{{Text: "first"}, {NoData: true}},
			}}}},
			wantProvenance: []Provenance{{Source: "k8s", Fixture: 0, Response: 0, Seq: 1}, {Source: "k8s", Fixture: 0, Response: 1, Seq: 2}},
		},
		{
			name: "a source's own no-data text is kept",
			calls: []investigator.RecordedCall{
				call(1, "github", "list_commits", "no_data", `{"repo":"a/b"}`, "No commits since the requested time."),
			},
			want: map[string]mock.Config{"github": {Fixtures: []mock.Fixture{{
				Tool: "list_commits", Match: mock.Match{Args: map[string]mock.Matcher{"repo": {Equals: "a/b"}}},
				Respond: &mock.Response{Text: "No commits since the requested time.", NoData: true},
			}}}},
			wantProvenance: []Provenance{{Source: "github", Fixture: 0, Seq: 1}},
		},
		{
			name: "a source whose calls all failed to answer is unavailable",
			calls: []investigator.RecordedCall{
				call(1, "k8s", "get_pods", "unavailable", `{}`, "down"),
				call(2, "k8s", "get_pods", "unavailable", `{}`, "down"),
			},
			want:            map[string]mock.Config{"k8s": {Unavailable: true}},
			wantUnavailable: []string{"k8s"},
		},
		{
			name: "calls that errored are left out, the source stays",
			calls: []investigator.RecordedCall{
				call(1, "github", "list_commits", "error", `{}`, "boom"),
				call(2, "github", "list_commits", "timeout", `{}`, "slow"),
			},
			want: map[string]mock.Config{"github": {}},
		},
		{
			name: "evidence with an unknown kind becomes other, and one without a title uses its URL",
			calls: []investigator.RecordedCall{{
				Seq: 1, CallID: "c", Source: "s", Tool: "t", Status: "ok", Input: json.RawMessage(`{}`), Text: "x",
				Evidence: []source.Evidence{{Kind: "weird", Title: "T", URL: "https://x.test/1"}, {URL: "https://x.test/2"}, {}},
			}},
			want: map[string]mock.Config{"s": {Fixtures: []mock.Fixture{{Tool: "t", Respond: &mock.Response{Text: "x", Evidence: []mock.Evidence{
				{Kind: source.EvidenceOther, Title: "T", URL: "https://x.test/1"},
				{Kind: source.EvidenceOther, Title: "https://x.test/2", URL: "https://x.test/2"},
			}}}}}},
			wantProvenance: []Provenance{{Source: "s", Fixture: 0, Seq: 1}},
		},
		{name: "no calls", wantErr: ErrNoSourceCalls},
		{
			name:    "input that is not an object",
			calls:   []investigator.RecordedCall{call(1, "s", "t", "ok", `[1]`, "x")},
			wantErr: errAny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, unavailable, provenance, _, err := fixturesOf(tt.calls)
			if tt.wantErr != nil {
				require.Error(t, err)

				if tt.wantErr != errAny {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantUnavailable, unavailable)
			require.Equal(t, tt.wantProvenance, provenance)
		})
	}
}
