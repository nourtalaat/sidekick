package flags

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

// testScope and testWindow are what the result tests read.
var (
	testScope  = Scope{Project: "shop", Environment: "production"}
	testWindow = Window{Start: testNow.Add(-time.Hour), End: testNow}
)

func TestChangeListResult(t *testing.T) {
	t.Parallel()

	evidence := []source.Evidence{{Kind: source.EvidenceDeploy, Title: "flag", URL: "https://flags.example.com/f"}}
	change := Change{Time: testNow.Add(-time.Minute), Flag: "new-checkout", Name: "New checkout", Action: "turned on the flag",
		Actor: "ada@example.com", Summary: "- Turned on the flag", Comment: "rollout", URL: "https://flags.example.com/f"}

	tests := []struct {
		name          string
		flag          string
		changes       []Change
		more          bool
		evidence      []source.Evidence
		want          string
		wantTruncated bool
		wantText      string
	}{
		{
			name:     "changes become JSON with the range",
			changes:  []Change{change},
			evidence: evidence,
			want: `{"project":"shop","environment":"production","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z",
				"changes":[{"time":"2026-05-04T09:59:00Z","flag":"new-checkout","name":"New checkout","action":"turned on the flag",
				"actor":"ada@example.com","summary":"- Turned on the flag","comment":"rollout","url":"https://flags.example.com/f"}]}`,
		},
		{
			name:    "more changes left is a cut result",
			flag:    "new-checkout",
			changes: []Change{{Time: testNow.Add(-time.Minute), Flag: "new-checkout"}},
			more:    true,
			want: `{"project":"shop","environment":"production","flag":"new-checkout","since":"2026-05-04T09:00:00Z",
				"until":"2026-05-04T10:00:00Z","changes":[{"time":"2026-05-04T09:59:00Z","flag":"new-checkout"}]}`,
			wantTruncated: true,
		},
		{
			name:     "no changes is no data, with the evidence",
			evidence: evidence,
			wantText: "No flag changes in LaunchDarkly project shop, environment production, between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
		},
		{
			name:     "no changes to one flag names the flag",
			flag:     "new-checkout",
			wantText: "No changes to flag new-checkout in LaunchDarkly project shop, environment production, between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := NewChangeList(testScope, tt.flag, testWindow)
			for _, c := range tt.changes {
				require.True(t, l.Add(c))
			}

			require.Equal(t, len(tt.changes), l.Len())

			res, err := l.Result("LaunchDarkly", tt.more, tt.evidence)
			require.NoError(t, err)
			require.Equal(t, tt.evidence, res.Evidence)

			if tt.wantText != "" {
				require.True(t, res.NoData)
				require.Equal(t, tt.wantText, res.Text)
				require.Empty(t, res.Structured)

				return
			}

			require.False(t, res.NoData)
			require.JSONEq(t, tt.want, string(res.Structured))
			require.Equal(t, tt.wantTruncated, res.Truncated)
		})
	}
}

func TestChangeListCutsLongText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		changes int
		summary int
		comment int
		link    int
		// wantAll says every change fits.
		wantAll bool
	}{
		{name: "a long summary and comment are cut", changes: 1, summary: MaxTextLen + 100, comment: MaxTextLen * 3, wantAll: true},
		{name: "a link too long to keep is left out", changes: 1, link: maxURLLen + 1, wantAll: true},
		{name: "the list stops before it grows too large", changes: MaxResultSize / MaxTextLen, summary: MaxTextLen, comment: MaxTextLen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := NewChangeList(testScope, "", testWindow)
			added := 0

			for range tt.changes {
				c := Change{
					Time:    testNow,
					Flag:    strings.Repeat("f", MaxNameLen+10),
					Summary: strings.Repeat("é", tt.summary/2),
					Comment: strings.Repeat("x", tt.comment),
					URL:     "https://flags.example.com/" + strings.Repeat("a", tt.link),
				}

				if !l.Add(c) {
					break
				}

				added++
			}

			require.Positive(t, added)
			require.Equal(t, tt.wantAll, added == tt.changes)

			if !tt.wantAll {
				require.False(t, l.Add(Change{}), "a full list takes no more")
			}

			res, err := l.Result("LaunchDarkly", false, nil)
			require.NoError(t, err)
			require.True(t, res.Truncated)
			require.LessOrEqual(t, len(res.Structured), MaxResultSize+added+len(`{"project":"shop","environment":"production",`+
				`"since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z","changes":[]}`))

			var got Changes
			require.NoError(t, json.Unmarshal(res.Structured, &got))

			for _, c := range got.Changes {
				require.LessOrEqual(t, len(c.Flag), MaxNameLen)
				require.LessOrEqual(t, len(c.Summary), MaxTextLen)
				require.LessOrEqual(t, len(c.Comment), MaxTextLen)
				require.LessOrEqual(t, len(c.URL), maxURLLen)
			}
		})
	}
}

func TestStateResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		state         State
		want          string
		wantTruncated bool
	}{
		{
			name: "a flag that is on",
			state: State{Project: "shop", Environment: "production", Flag: "new-checkout", Name: "New checkout", Kind: "boolean",
				On: true, Rules: 2, Targets: 3, Fallthrough: "variation 0: true", OffVariation: "variation 1: false",
				LastChanged: testNow, Version: 7, URL: "https://flags.example.com/f"},
			want: `{"project":"shop","environment":"production","flag":"new-checkout","name":"New checkout","kind":"boolean",
				"on":true,"rules":2,"targets":3,"fallthrough":"variation 0: true","offVariation":"variation 1: false",
				"lastChanged":"2026-05-04T10:00:00Z","version":7,"url":"https://flags.example.com/f"}`,
		},
		{
			name:  "a flag that is off keeps on and rules",
			state: State{Project: "shop", Environment: "production", Flag: "f"},
			want:  `{"project":"shop","environment":"production","flag":"f","on":false,"rules":0}`,
		},
		{
			name:          "a long name is cut",
			state:         State{Project: "shop", Environment: "production", Flag: "f", Name: strings.Repeat("n", MaxNameLen+1)},
			want:          `{"project":"shop","environment":"production","flag":"f","name":"` + strings.Repeat("n", MaxNameLen) + `","on":false,"rules":0}`,
			wantTruncated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "flag"}}

			res, err := tt.state.Result(evidence)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(res.Structured))
			require.Equal(t, tt.wantTruncated, res.Truncated)
			require.Equal(t, evidence, res.Evidence)
			require.False(t, res.NoData)
		})
	}
}

func TestTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tool         string
		wantRequired []string
	}{
		{name: "list_flag_changes needs since", tool: ToolListChanges, wantRequired: []string{"since"}},
		{name: "get_flag needs flag", tool: ToolGetFlag, wantRequired: []string{"flag"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			list := Tools("LaunchDarkly")
			require.Len(t, list, 2)

			for _, tool := range list {
				if tool.Name != tt.tool {
					continue
				}

				require.True(t, tool.ReadOnly)
				require.Contains(t, tool.Description, "LaunchDarkly")

				var s struct {
					Properties map[string]any `json:"properties"`
					Required   []string       `json:"required"`
				}

				require.NoError(t, json.Unmarshal(tool.InputSchema, &s))
				require.Equal(t, tt.wantRequired, s.Required)
				require.Contains(t, s.Properties, "project")
				require.Contains(t, s.Properties, "environment")
				require.Contains(t, s.Properties, "flag")

				return
			}

			t.Fatalf("no tool %s", tt.tool)
		})
	}
}
