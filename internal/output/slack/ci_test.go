package slack

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

func TestCIReplyView(t *testing.T) {
	t.Parallel()

	const prLink = "<https://github.com/acme/monitors/pull/12|acme/monitors #12>"

	named := func(c output.CIReport) output.CIReport {
		c.Repo, c.Number, c.URL = "acme/monitors", 12, "https://github.com/acme/monitors/pull/12"

		return c
	}

	tests := []struct {
		name      string
		report    output.CIReport
		wantText  string
		wantColor string
		want      string
	}{
		{
			name:      "passed",
			report:    named(output.CIReport{State: output.CIPassed, Passed: 7, Total: 7}),
			wantText:  "CI passed on acme/monitors #12",
			wantColor: colorCIPassed,
			want:      ":white_check_mark: *CI passed* on " + prLink + "\nAll 7 checks passed.",
		},
		{
			name:      "passed with one check",
			report:    named(output.CIReport{State: output.CIPassed, Passed: 1, Total: 1}),
			wantText:  "CI passed on acme/monitors #12",
			wantColor: colorCIPassed,
			want:      ":white_check_mark: *CI passed* on " + prLink + "\nThe check passed.",
		},
		{
			name: "failed lists the checks by name, with links",
			report: named(output.CIReport{
				State: output.CIFailed, Passed: 5, Total: 7, FailedCount: 2,
				Failed: []output.CICheck{{Name: "unit tests", URL: "https://ci.example.com/1"}, {Name: "lint", URL: "javascript:alert(1)"}},
			}),
			wantText:  "CI failed on acme/monitors #12",
			wantColor: colorCIFailed,
			want: ":x: *CI failed* on " + prLink + "\n2 of 7 checks failed:\n" +
				"• <https://ci.example.com/1|unit tests>\n• lint",
		},
		{
			name: "failed says how many more there are",
			report: named(output.CIReport{
				State: output.CIFailed, Total: 20, FailedCount: 13,
				Failed: []output.CICheck{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"}, {Name: "f"}, {Name: "g"}, {Name: "h"}, {Name: "i"}, {Name: "j"}},
			}),
			wantText:  "CI failed on acme/monitors #12",
			wantColor: colorCIFailed,
			want: ":x: *CI failed* on " + prLink + "\n13 of 20 checks failed:\n" +
				"• a\n• b\n• c\n• d\n• e\n• f\n• g\n• h\n• i\n• j\nand 3 more",
		},
		{
			name:      "none",
			report:    named(output.CIReport{State: output.CINone, WaitedSeconds: 600}),
			wantText:  "No CI ran on acme/monitors #12",
			wantColor: colorCINeutral,
			want:      ":grey_question: *No CI ran* on " + prLink + "\nNo check or pipeline started within 10 minutes. The repository may have no CI for this branch.",
		},
		{
			name:      "unknown shows Sidekick's own sentence",
			report:    named(output.CIReport{State: output.CIUnknown, Message: "Sidekick could not read the checks. The GitHub App needs Checks (read) and Commit statuses (read)."}),
			wantText:  "CI could not be read on acme/monitors #12",
			wantColor: colorFailed,
			want:      ":warning: *CI could not be read* on " + prLink + "\nSidekick could not read the checks. The GitHub App needs Checks (read) and Commit statuses (read).",
		},
		{
			name:      "timed out",
			report:    named(output.CIReport{State: output.CITimedOut, Passed: 5, Total: 7, WaitedSeconds: 7200}),
			wantText:  "CI had not finished on acme/monitors #12",
			wantColor: colorFailed,
			want:      ":hourglass: *CI had not finished* on " + prLink + "\nAfter 2 hours, 5 of 7 checks had passed and 2 were still running.",
		},
		{
			name:      "a report that does not name the request",
			report:    output.CIReport{State: output.CIPassed, Total: 2},
			wantText:  "CI passed",
			wantColor: colorCIPassed,
			want:      ":white_check_mark: *CI passed*\nAll 2 checks passed.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			v := ciReplyView(tt.report, permalinkOK)

			require.Equal(t, tt.wantText, v.text)
			require.Equal(t, tt.wantColor, v.attachment.Color)
			require.Equal(t, tt.want, v.attachment.Blocks[0].Text.Text)
			require.Contains(t, v.attachment.Blocks[1].Elements[0].Text, "See the investigation")
		})
	}
}

func TestCIReplyShowsOutsideTextAsPlainText(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 200)

	v := ciReplyView(output.CIReport{
		State: output.CIFailed, Total: 3, FailedCount: 3,
		Repo: "acme/<!channel>", Number: 1, URL: "https://github.com/acme/monitors/pull/1",
		Failed: []output.CICheck{
			{Name: "<!channel> & <https://evil.example|click>", URL: "https://ci.example.com/a"},
			{Name: "*bold* _it_ ~gone~ `code`"},
			{Name: long},
		},
	}, "")

	text := v.attachment.Blocks[0].Text.Text

	require.NotContains(t, text, "<!channel>")
	require.Contains(t, text, "acme/&lt;!channel&gt; #1")
	require.Contains(t, text, "<https://ci.example.com/a|&lt;!channel&gt; &amp; &lt;https://evil.example|click&gt;>")
	require.Contains(t, text, "• bold it gone code")
	require.NotContains(t, text, long, "a long name is cut")
	require.Contains(t, text, fmt.Sprintf("• %s…", strings.Repeat("x", maxCheckNameChars-1)))
}
