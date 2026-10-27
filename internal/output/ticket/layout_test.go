package ticket

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestLayout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		edit  func(u *output.Update)
		check func(t *testing.T, d Doc)
	}{
		{
			name: "a real issue in the order of the Slack message",
			edit: func(u *output.Update) {
				u.Events = 3
				u.URL = "https://sidekick.example.com/investigations/1"
				u.Report.Cause, u.Report.Impact = "The 10:02 deploy.", "Checkout is slow."
				u.Report.Findings = []report.Finding{{Claim: "Calls doubled.", Evidence: []source.Evidence{{Title: "Query", URL: "https://signoz.example.com/q/1"}}}}
				u.Report.Fixes = []report.Fix{{Title: "Roll back", Detail: "Deploy 4f2a."}}
				u.Report.Checked = []report.Check{{Source: "signoz", Status: report.CheckOK}, {Source: "github", Status: report.CheckFailed}}
			},
			check: func(t *testing.T, d Doc) {
				t.Helper()
				require.Equal(t, []string{"", "", "Likely cause", "Impact", "Findings", "Proposed fixes", "", ""}, headings(d))
				require.Equal(t, Line{
					{Text: "🚨 Real issue", Bold: true},
					{Text: " · High confidence"},
					{Text: " · Sidekick investigated checkout-api p99 latency above 2s (3 events)"},
				}, d.Blocks[0].Lines[0])
				require.Equal(t, Line{{Text: "Calls doubled."}, {Text: " · "}, {Text: "Query", URL: "https://signoz.example.com/q/1"}}, d.Blocks[4].Lines[0])
				require.Equal(t, Line{{Text: "Roll back", Bold: true}, {Text: ": Deploy 4f2a."}}, d.Blocks[5].Lines[0])
				require.Equal(t, Numbered, d.Blocks[5].Kind)
				require.Equal(t, "Checked signoz · github did not respond", d.Blocks[6].Lines[0][0].Text)
				require.Equal(t, "https://sidekick.example.com/investigations/1", d.Blocks[7].Lines[0][0].URL)
			},
		},
		{
			name: "a false alarm says why it fired and how to stop it",
			edit: func(u *output.Update) {
				u.Report.Verdict = report.FalsePositive
				u.Report.Cause = "not shown"
				u.Report.FalsePositive = &report.FalsePositiveDetail{
					WhyFired:   "A batch job.",
					Prevention: []report.Prevention{{Kind: report.PreventDuration, Detail: "Wait 10m."}, {Kind: report.PreventOther}},
				}
			},
			check: func(t *testing.T, d Doc) {
				t.Helper()
				require.Equal(t, []string{"", "", "Why it fired", "How to stop it"}, headings(d))
				require.Equal(t, []Line{{{Text: "Wait 10m."}}}, d.Blocks[3].Lines)
			},
		},
		{
			name: "not sure yet shows no confidence",
			edit: func(u *output.Update) { u.Report.Verdict = report.Inconclusive },
			check: func(t *testing.T, d Doc) {
				t.Helper()
				require.Equal(t, Line{{Text: "❔ Not sure yet", Bold: true}, {Text: " · Sidekick investigated checkout-api p99 latency above 2s"}}, d.Blocks[0].Lines[0])
			},
		},
		{
			name: "a run without a report could not finish",
			edit: func(u *output.Update) { u.Report = nil },
			check: func(t *testing.T, d Doc) {
				t.Helper()
				require.Equal(t, "⚠️ Could not finish", d.Blocks[0].Lines[0][0].Text)
				require.Equal(t, "The investigation stopped with an error.", d.Blocks[1].Lines[0][0].Text)
				require.Len(t, d.Blocks, 2)
			},
		},
		{
			name: "a run that stopped with an error says why and what it checked",
			edit: func(u *output.Update) {
				u.Report.StopReason, u.Report.StopDetail = report.StopError, "The model provider was down."
				u.Report.Checked = []report.Check{{Source: "k8s", Status: report.CheckNoData}}
			},
			check: func(t *testing.T, d Doc) {
				t.Helper()
				require.Equal(t, "The model provider was down.", d.Blocks[1].Lines[0][0].Text)
				require.Equal(t, Span{Text: "Checked k8s", Italic: true}, d.Blocks[2].Lines[0][0])
			},
		},
		{
			name: "hypotheses, unsafe evidence links and too many findings",
			edit: func(u *output.Update) {
				u.Report.Findings = []report.Finding{
					{Claim: "Maybe the cache."},
					{Claim: "Links.", Evidence: []source.Evidence{
						{Kind: "query", URL: "javascript:alert(1)"},
						{Kind: "query", URL: "https://a.example.com/1"},
						{Title: "2", URL: "https://a.example.com/2"},
						{Title: "3", URL: "https://a.example.com/3"},
						{Title: "4", URL: "https://a.example.com/4"},
					}},
				}
				for i := range maxFindings {
					u.Report.Findings = append(u.Report.Findings, report.Finding{Claim: fmt.Sprint(i), Evidence: []source.Evidence{{Kind: "log"}}})
				}
			},
			check: func(t *testing.T, d Doc) {
				t.Helper()
				lines := d.Blocks[2].Lines
				require.Len(t, lines, maxFindings+1)
				require.Equal(t, Line{{Text: "Hypothesis, no evidence yet: ", Italic: true}, {Text: "Maybe the cache."}}, lines[0])
				require.Equal(t, Line{
					{Text: "Links."},
					{Text: " · "}, {Text: "query", URL: "https://a.example.com/1"},
					{Text: " · "}, {Text: "2", URL: "https://a.example.com/2"},
					{Text: " · "}, {Text: "3", URL: "https://a.example.com/3"},
				}, lines[1])
				require.Equal(t, Line{{Text: "2 more in the investigation", Italic: true}}, lines[maxFindings])
			},
		},
		{
			name: "a page URL that is not http is not linked",
			edit: func(u *output.Update) { u.URL = "ftp://sidekick.example.com/1" },
			check: func(t *testing.T, d Doc) {
				t.Helper()
				require.Len(t, d.Blocks, 2)
			},
		},
		{
			name: "a long title is cut and kept on one line",
			edit: func(u *output.Update) { u.Event.Title = "line one\nline two " + strings.Repeat("x", maxTitleBytes) },
			check: func(t *testing.T, d Doc) {
				t.Helper()
				about := d.Blocks[0].Lines[0][2].Text
				require.True(t, strings.HasPrefix(about, " · Sidekick investigated line one line two x"))
				require.True(t, strings.HasSuffix(about, ellipsis))
				require.LessOrEqual(t, len(strings.TrimPrefix(about, " · Sidekick investigated ")), maxTitleBytes)
			},
		},
		{
			name: "no title falls back to the alert name, then to the alert",
			edit: func(u *output.Update) { u.Event = event.Event{} },
			check: func(t *testing.T, d Doc) {
				t.Helper()
				require.Equal(t, " · Sidekick investigated the alert", d.Blocks[0].Lines[0][2].Text)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := finalUpdate()
			tt.edit(&u)

			tt.check(t, Layout(u))
		})
	}
}

func TestCut(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{name: "short", in: "  fits  ", limit: 10, want: "fits"},
		{name: "exact", in: "abcdef", limit: 6, want: "abcdef"},
		{name: "long", in: "abcdefghij", limit: 7, want: "abcd" + ellipsis},
		{name: "on a character boundary", in: "ééééé", limit: 8, want: "éé" + ellipsis},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := cut(tt.in, tt.limit)
			require.Equal(t, tt.want, got)
			require.LessOrEqual(t, len(got), tt.limit)
		})
	}
}

// headings returns the heading of each block of d.
func headings(d Doc) []string {
	out := make([]string, 0, len(d.Blocks))
	for _, b := range d.Blocks {
		out = append(out, b.Heading)
	}

	return out
}
