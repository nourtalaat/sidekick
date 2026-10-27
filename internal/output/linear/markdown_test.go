package linear

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/output/ticket"
	"github.com/nourtalaat/sidekick/internal/report"
)

// update rewrites the golden files: go test ./internal/output/linear -update.
var update = flag.Bool("update", false, "rewrite the golden files in fixture/golden/")

func TestRenderGolden(t *testing.T) {
	t.Parallel()

	falseAlarm := finalUpdate()
	falseAlarm.Report.Verdict = report.FalsePositive
	falseAlarm.Report.FalsePositive = &report.FalsePositiveDetail{
		WhyFired:   "A nightly batch job raises p99 for two minutes.",
		Prevention: []report.Prevention{{Kind: report.PreventDuration, Detail: "Fire only after 10 minutes above 2s."}},
	}

	failed := finalUpdate()
	failed.Report = nil

	tests := []struct {
		name string
		u    output.Update
	}{
		{name: "true-positive", u: finalUpdate()},
		{name: "false-alarm", u: falseAlarm},
		{name: "failed", u: failed},
		{name: "untrusted", u: untrustedUpdate()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := render(ticket.Layout(tt.u))

			path := filepath.Join("fixture", "golden", tt.name+".md")
			if *update {
				require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
			}

			want, err := os.ReadFile(path) //nolint:gosec // A fixture path chosen by the test.
			require.NoError(t, err, "run go test ./internal/output/linear -update to create the golden file")
			require.Equal(t, string(want), got)
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
		{name: "plain words", in: "Checkout is slow", want: "Checkout is slow"},
		{name: "a link", in: "[x](https://e.com)", want: `\[x\]\(https\://e.com\)`},
		{name: "an image", in: "![x](y)", want: `\!\[x\]\(y\)`},
		{name: "HTML", in: "<b>hi</b>", want: `\<b\>hi\</b\>`},
		{name: "emphasis and code", in: "*a* _b_ `c` ~~d~~", want: "\\*a\\* \\_b\\_ \\`c\\` \\~\\~d\\~\\~"},
		{name: "a mention and an emoji code", in: "@here :tada:", want: `\@here \:tada\:`},
		{name: "a heading, a quote and a table", in: "# h\n> q | t", want: "\\# h\n\\> q \\| t"},
		{name: "a numbered list item", in: "1. one\n  22. two\n3 items", want: "1\\. one\n  22\\. two\n3 items"},
		{name: "an entity and a backslash", in: `&amp; \`, want: `\&amp; \\`},
		{name: "carriage returns", in: "a\r\nb", want: "a\nb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, escape(tt.in))
		})
	}
}

func TestRenderSpan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		span ticket.Span
		want string
	}{
		{name: "plain", span: ticket.Span{Text: "a b"}, want: "a b"},
		{name: "bold", span: ticket.Span{Text: "Real issue", Bold: true}, want: "**Real issue**"},
		{name: "italic with a trailing space", span: ticket.Span{Text: "Hypothesis: ", Italic: true}, want: "_Hypothesis\\:_ "},
		{name: "bold and italic", span: ticket.Span{Text: " x ", Bold: true, Italic: true}, want: " **_x_** "},
		{name: "only spaces", span: ticket.Span{Text: "  ", Bold: true}, want: "  "},
		{name: "a link", span: ticket.Span{Text: "q [1]", URL: "https://e.com/a (b)"}, want: `[q \[1\]](https://e.com/a%20%28b%29)`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, renderSpan(tt.span, tt.span.Text))
		})
	}
}
