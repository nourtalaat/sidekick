package slack

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// update rewrites the golden files: go test ./internal/output/slack -update.
var update = flag.Bool("update", false, "rewrite the golden files in fixture/golden/")

// receivedAt is when the test event arrived.
var receivedAt = time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC)

func TestRenderGolden(t *testing.T) {
	t.Parallel()

	for _, tt := range renderedViews(t) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := goldenJSON(t, tt.view)

			path := filepath.Join("fixture", "golden", tt.name+".json")
			if *update {
				require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
			}

			want, err := os.ReadFile(path) //nolint:gosec // A fixture path chosen by the test.
			require.NoError(t, err, "run go test ./internal/output/slack -update to create the golden file")
			require.Equal(t, string(want), got)
		})
	}
}

func TestNoViewCarriesTheTestAlertBlockID(t *testing.T) {
	t.Parallel()

	for _, tt := range renderedViews(t) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NotContains(t, goldenJSON(t, tt.view), TestAlertBlockID)
			require.NotContains(t, tt.view.text, TestAlertBlockID)
		})
	}
}

// rendered is one view with the name of its golden file.
type rendered struct {
	name string
	view view
}

// renderedViews renders every kind of message the sink posts: each phase,
// each verdict, the notices, the watcher lines and the replies.
func renderedViews(t *testing.T) []rendered {
	t.Helper()

	cases := []rendered{
		{name: "queued", view: mainView(testUpdate(output.PhaseQueued, nil))},
		{name: "running", view: mainView(runningUpdate(3))},
		{name: "running-with-many-steps", view: mainView(runningUpdate(maxProgressLines + 2))},
		{name: "failed", view: mainView(testUpdate(output.PhaseFinal, &report.Report{
			StopReason: report.StopError,
			StopDetail: "The model returned an error after 3 tries.",
			Checked:    []report.Check{{Source: "signoz", Status: report.CheckOK}},
		}))},
		{name: "failed-without-report", view: mainView(testUpdate(output.PhaseFinal, nil))},
		{name: "false-alarm-fields", view: mainView(testUpdate(output.PhaseFinal, falseAlarm()))},
		{name: "untrusted-text", view: mainView(untrustedUpdate())},
		{name: "no-verdict", view: mainView(testUpdate(output.PhaseFinal, noVerdict()))},
		{name: "triage-only", view: mainView(testUpdate(output.PhaseFinal, triageOnly()))},
		{name: "notice", view: noticeView(output.Notice{
			Kind:  output.NoticeDailyCap,
			Title: "Daily cap reached",
			Text:  "Route payments has used all of today's $40.00 cap. New investigations on this route wait until 00:00 UTC.",
		})},
	}

	for _, v := range []report.Verdict{report.TruePositive, report.FalsePositive, report.Inconclusive, report.Duplicate, report.KnownIssue, report.Flapping} {
		u := testUpdate(output.PhaseFinal, verdictReport(v))
		cases = append(cases,
			rendered{name: "final-" + string(v), view: mainView(u)},
			rendered{name: "reply-" + string(v), view: verdictReplyView(u, "https://example.slack.com/archives/C123/p1793023321000200")},
		)
	}

	long := testUpdate(output.PhaseFinal, longReport())
	more, ok := continuationView(long)
	require.True(t, ok)

	cases = append(cases,
		rendered{name: "long-report", view: mainView(long)},
		rendered{name: "long-report-continued", view: more},
		rendered{name: "reply-without-permalink", view: verdictReplyView(long, "")},
	)

	watchers := testWatchers()
	running := runningUpdate(3)
	running.Watchers = watchers[:1]
	all := testUpdate(output.PhaseRunning, nil)
	all.Watchers = watchers

	cases = append(cases,
		rendered{name: "running-with-watchers", view: mainView(running)},
		rendered{name: "watcher-lines", view: view{attachment: attachment{Blocks: slices.Concat(watcherContext(all), watcherButtons(all))}}},
	)

	for name, w := range watcherReplies() {
		u := testUpdate(output.PhaseWatcher, nil)
		u.Watchers, u.Watcher = watchers, &w
		cases = append(cases, rendered{name: "watcher-reply-" + name, view: watcherReplyView(u, "https://example.slack.com/archives/C123/p1793023321000200")})
	}

	return cases
}

func TestEveryVerdictShowsAnEmojiAndALabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		verdict   report.Verdict
		wantEmoji string
		wantLabel string
		wantColor string
	}{
		{verdict: report.TruePositive, wantEmoji: ":rotating_light:", wantLabel: "Real issue", wantColor: "#C4261C"},
		{verdict: report.FalsePositive, wantEmoji: ":no_bell:", wantLabel: "False alarm", wantColor: "#1F8A4C"},
		{verdict: report.Inconclusive, wantEmoji: ":grey_question:", wantLabel: "Not sure yet", wantColor: "#7C85A3"},
		{verdict: report.Duplicate, wantEmoji: ":link:", wantLabel: "Duplicate", wantColor: "#2C57CD"},
		{verdict: report.KnownIssue, wantEmoji: ":pushpin:", wantLabel: "Known issue", wantColor: "#2C57CD"},
		{verdict: report.Flapping, wantEmoji: ":repeat:", wantLabel: "Flapping", wantColor: "#17998D"},
	}

	for _, tt := range tests {
		t.Run(string(tt.verdict), func(t *testing.T) {
			t.Parallel()

			u := testUpdate(output.PhaseFinal, verdictReport(tt.verdict))

			for _, v := range []view{mainView(u), verdictReplyView(u, "")} {
				require.Equal(t, tt.wantColor, v.attachment.Color)
				require.True(t, strings.HasPrefix(v.attachment.Blocks[0].Text.Text, tt.wantEmoji+" *"+tt.wantLabel+"*"),
					"the first block starts with the emoji and the label: %q", v.attachment.Blocks[0].Text.Text)
				require.True(t, strings.HasPrefix(v.text, tt.wantLabel), "the notification text starts with the label: %q", v.text)
			}
		})
	}
}

func TestRunningBarIsBrandOrange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase    output.Phase
		wantText string
	}{
		{phase: output.PhaseQueued, wantText: "Looking into it: checkout-api p99 latency above 2s"},
		{phase: output.PhaseRunning, wantText: "Investigating: checkout-api p99 latency above 2s"},
	}

	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			t.Parallel()

			v := mainView(testUpdate(tt.phase, nil))
			require.Equal(t, colorRunning, v.attachment.Color)
			require.Equal(t, tt.wantText, v.text)
		})
	}
}

func TestContinuationOnlyForLongReports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		report *report.Report
		want   bool
	}{
		{name: "short report", report: verdictReport(report.TruePositive)},
		{name: "long report", report: longReport(), want: true},
		{name: "failed run", report: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, got := continuationView(testUpdate(output.PhaseFinal, tt.report))
			require.Equal(t, tt.want, got)
		})
	}
}

func TestEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "a channel mention", in: "<!channel> look", want: "&lt;!channel&gt; look"},
		{name: "a user mention", in: "ping <@U123>", want: "ping &lt;@U123&gt;"},
		{name: "a link", in: "<https://evil.example|click>", want: "&lt;https://evil.example|click&gt;"},
		{name: "an ampersand", in: "a & b", want: "a &amp; b"},
		{name: "an escape that is already there", in: "&lt;", want: "&amp;lt;"},
		{name: "plain text", in: "p99 rose to 2.6 s", want: "p99 rose to 2.6 s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, escape(tt.in))
		})
	}
}

func TestLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		url   string
		title string
		want  string
	}{
		{name: "https", url: "https://signoz.example.com/d/abc", title: "SigNoz", want: "<https://signoz.example.com/d/abc|SigNoz>"},
		{name: "query with ampersand", url: "https://logs.example.com/?q=a&from=1", title: "Logs", want: "<https://logs.example.com/?q=a&amp;from=1|Logs>"},
		{name: "characters that would end the link", url: "https://x.example.com/a|b>c", title: "x", want: "<https://x.example.com/a%7Cb%3Ec|x>"},
		{name: "title with a mention", url: "https://x.example.com/", title: "<!here>", want: "<https://x.example.com/|&lt;!here&gt;>"},
		{name: "javascript", url: "javascript:alert(1)", title: "x"},
		{name: "relative", url: "/d/abc", title: "x"},
		{name: "mailto", url: "mailto:a@example.com", title: "x"},
		{name: "empty", title: "x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, link(tt.url, tt.title))
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
		{name: "short", in: "abc", limit: 5, want: "abc"},
		{name: "exact", in: "abcde", limit: 5, want: "abcde"},
		{name: "long", in: "abcdefgh", limit: 5, want: "abcd…"},
		{name: "characters, not bytes", in: "ééééééé", limit: 3, want: "éé…"},
		{name: "space trimmed", in: "  ab  ", limit: 5, want: "ab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, cut(tt.in, tt.limit))
		})
	}
}

// goldenJSON returns v as Slack gets it, indented and without HTML
// escapes, so the golden files are easy to read.
func goldenJSON(t *testing.T, v view) string {
	t.Helper()

	var b strings.Builder

	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(struct {
		Text        string       `json:"text"`
		Attachments []attachment `json:"attachments"`
	}{Text: v.text, Attachments: []attachment{v.attachment}}))

	return b.String()
}

// testUpdate returns an update of phase for the test event, with r.
func testUpdate(phase output.Phase, r *report.Report) output.Update {
	return output.Update{
		InvestigationID: "0192d3a4-0000-7000-8000-0000000000aa",
		Event: event.Event{
			Intake:     "alertmanager",
			Kind:       event.KindAlert,
			AlertName:  "CheckoutLatencyP99",
			Title:      "checkout-api p99 latency above 2s",
			Service:    "checkout-api",
			ReceivedAt: receivedAt,
		},
		Events: 1,
		Phase:  phase,
		Report: r,
		Seq:    1,
	}
}

// runningUpdate returns a running update with n progress lines, in a group
// of 3 events.
func runningUpdate(n int) output.Update {
	u := testUpdate(output.PhaseRunning, nil)
	u.Events = 3

	for i := range n {
		u.Progress = append(u.Progress, output.Progress{At: receivedAt.Add(time.Duration(i) * time.Second), Text: fmt.Sprintf("Step %d done", i+1)})
	}

	return u
}

// verdictReport returns a short report with verdict v.
func verdictReport(v report.Verdict) *report.Report {
	return &report.Report{
		Verdict:    v,
		Confidence: report.High,
		Reason:     "Deploy 4f2c1a went out at 14:01. p99 latency rose from 400 ms to 2.6 s about 90 seconds later.",
		Cause:      "New retry loop in the payment client",
		Impact:     "About 4% of checkouts fail",
		Findings: []report.Finding{
			{ID: "f1", Claim: "p99 rose at 14:02, 90 s after the deploy.", Evidence: []source.Evidence{
				{Kind: source.EvidenceMetric, Title: "SigNoz", URL: "https://signoz.example.com/d/abc"},
			}},
			{ID: "f2", Claim: "Timeouts to payments-gw in the logs from 14:02.", Evidence: []source.Evidence{
				{Kind: source.EvidenceLogQuery, Title: "Logs", URL: "https://signoz.example.com/l/def?from=1&to=2"},
				{Kind: source.EvidenceLogQuery, Title: "No URL"},
			}},
			{ID: "f3", Claim: "The gateway itself is fine"},
		},
		Fixes: []report.Fix{
			{ID: "x1", Title: "Roll back 4f2c1a", Detail: "Revert the deploy."},
			{ID: "x2", Title: "Cap retries at 2 in the payment client"},
		},
		Checked: []report.Check{
			{Source: "signoz", Status: report.CheckOK, ToolCalls: 4},
			{Source: "kubernetes", Status: report.CheckNoData},
			{Source: "github", Status: report.CheckUnavailable},
		},
		StopReason: report.StopSubmitted,
	}
}

// noVerdict returns the inconclusive report of a run whose model gave no
// report that passed its checks, built from its recorded findings.
func noVerdict() *report.Report {
	return &report.Report{
		Verdict:    report.Inconclusive,
		Confidence: report.Low,
		Reason:     "The model's report did not pass its checks 3 times, so this report holds only what the investigation recorded.",
		Findings: []report.Finding{
			{ID: "f1", Claim: "Disk use on db-1 rose from 70% to 93% since 09:00.", Evidence: []source.Evidence{
				{Kind: source.EvidenceOther, Title: "signoz search_logs", ToolCallID: "toolu_01"},
			}},
			{ID: "f2", Claim: "A backup job may have filled the disk."},
		},
		Checked:    []report.Check{{Source: "signoz", Status: report.CheckOK, ToolCalls: 2}},
		StopReason: report.StopNoVerdict,
		StopDetail: "invalid report: true_positive needs cause, impact and at least one fix",
	}
}

// triageOnly returns the report of a run that triage ended as a duplicate.
func triageOnly() *report.Report {
	return &report.Report{
		Verdict:    report.Duplicate,
		Confidence: report.High,
		Reason:     "The same alert as the open checkout-api latency incident.",
		StopReason: report.StopTriage,
		StopDetail: "Triage only: no full investigation ran.",
	}
}

// falseAlarm returns a false alarm report with its explanation.
func falseAlarm() *report.Report {
	r := verdictReport(report.FalsePositive)
	r.Cause, r.Impact = "", ""
	r.FalsePositive = &report.FalsePositiveDetail{
		WhyFired: "A nightly batch job raises p99 for two minutes.",
		Prevention: []report.Prevention{
			{Kind: report.PreventDuration, Detail: "Fire only after 5 minutes above 2 s."},
			{Kind: report.PreventExclusion, Detail: "Leave out the batch endpoint."},
		},
	}

	return r
}

// longReport returns a report with more findings and fixes than the main
// message shows, and one that stopped at its budget.
func longReport() *report.Report {
	r := verdictReport(report.TruePositive)
	r.Findings, r.Fixes = nil, nil

	for i := range maxFindings + 3 {
		r.Findings = append(r.Findings, report.Finding{
			ID:       fmt.Sprintf("f%d", i+1),
			Claim:    fmt.Sprintf("Finding %d", i+1),
			Evidence: []source.Evidence{{Kind: source.EvidenceMetric, Title: "Chart", URL: fmt.Sprintf("https://signoz.example.com/d/%d", i+1)}},
		})
	}

	for i := range maxFixes + 2 {
		r.Fixes = append(r.Fixes, report.Fix{ID: fmt.Sprintf("x%d", i+1), Title: fmt.Sprintf("Fix %d", i+1)})
	}

	r.StopReason = report.StopBudget

	return r
}

// untrustedUpdate returns a final update whose event and report text try
// to add mentions and links, and whose evidence has a URL that is not
// http.
func untrustedUpdate() output.Update {
	u := testUpdate(output.PhaseFinal, verdictReport(report.TruePositive))
	u.Event.Title = "<!channel> checkout & payments down"
	u.Report.Reason = "Ask <@U123> or see <https://evil.example|the fix>."
	u.Report.Cause = "<!here> deploy"
	u.Report.Findings = []report.Finding{{ID: "f1", Claim: "Claim with <b>", Evidence: []source.Evidence{
		{Kind: source.EvidenceOther, Title: "<!everyone>", URL: "javascript:alert(1)"},
		{Kind: source.EvidenceOther, Title: "Fine", URL: "https://x.example.com/a|b"},
	}}}
	u.Report.Fixes = []report.Fix{{ID: "x1", Title: "<tel:123|Call now>"}}

	return u
}

// testWatchers returns a watcher of the test event's group in each state:
// running, recovered, timed out, and stopped by a responder, when the
// group resolved and after a config change.
func testWatchers() []output.WatcherState {
	started := receivedAt.Add(30 * time.Second)
	stoppedAt := receivedAt.Add(6 * time.Minute)

	return []output.WatcherState{
		{
			ID: "0192d3a4-0000-7000-8000-0000000000d1", Name: "checkout-health", CheckType: "http", Status: output.WatcherRunning,
			PassesToRecover: 2, IntervalSeconds: 30, Checks: 4, LastResult: "HTTP 503", StartedAt: started,
		},
		{
			ID: "0192d3a4-0000-7000-8000-0000000000d2", Name: "alerts-cleared", CheckType: "alert", Status: output.WatcherRecovered,
			PassesInARow: 1, PassesToRecover: 1, IntervalSeconds: 30, Checks: 25, LastResult: "1 of 1 alerts recovered",
			StartedAt: started, EndedAt: receivedAt.Add(12*time.Minute + 30*time.Second),
		},
		{
			ID: "0192d3a4-0000-7000-8000-0000000000d3", Name: "payments-health", CheckType: "http", Status: output.WatcherTimedOut,
			PassesToRecover: 2, IntervalSeconds: 60, Checks: 60, LastResult: "HTTP 503 <!channel>",
			StartedAt: receivedAt, EndedAt: receivedAt.Add(time.Hour),
		},
		{
			ID: "0192d3a4-0000-7000-8000-0000000000d4", Name: "db-health", CheckType: "http", Status: output.WatcherStopped,
			PassesToRecover: 2, IntervalSeconds: 30, Checks: 12, LastResult: "HTTP 502", StartedAt: started, EndedAt: stoppedAt,
			StopReason: "responder", StoppedBy: &output.Person{SlackUserID: "U123"},
		},
		{
			ID: "0192d3a4-0000-7000-8000-0000000000d5", Name: "queue-depth", CheckType: "tool", Status: output.WatcherStopped,
			PassesToRecover: 2, IntervalSeconds: 120, Checks: 3, StartedAt: started, EndedAt: stoppedAt, StopReason: "group_resolved",
		},
		{
			ID: "0192d3a4-0000-7000-8000-0000000000d6", Name: "cache-health", CheckType: "http", Status: output.WatcherStopped,
			PassesToRecover: 2, IntervalSeconds: 30, Checks: 2, StartedAt: started, EndedAt: stoppedAt, StopReason: "config_changed",
		},
	}
}

// watcherReplies returns the changes the short replies announce, by the
// name of their golden file: a start, and the first three ends of
// testWatchers.
func watcherReplies() map[string]output.WatcherState {
	w := testWatchers()

	started := w[0]
	started.Checks, started.LastResult = 0, ""

	return map[string]output.WatcherState{
		"started":   started,
		"recovered": w[1],
		"timed-out": w[2],
		"stopped":   w[3],
	}
}

func TestMention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		person output.Person
		want   string
	}{
		{name: "a Slack user", person: output.Person{SlackUserID: "U01ABCDEF"}, want: "<@U01ABCDEF>"},
		{name: "a Slack user wins over the name", person: output.Person{SlackUserID: "U01ABCDEF", Name: "Ada"}, want: "<@U01ABCDEF>"},
		{name: "a web user is shown by name", person: output.Person{Name: "Ada Lovelace"}, want: "Ada Lovelace"},
		{name: "the name is escaped", person: output.Person{Name: "<!channel> & co"}, want: "&lt;!channel&gt; &amp; co"},
		{name: "an invalid Slack ID falls back to the name", person: output.Person{SlackUserID: "not an id", Name: "Ada"}, want: "Ada"},
		{name: "nobody", person: output.Person{}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, mention(tt.person))
		})
	}
}
