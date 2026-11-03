package gitlabgit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testFallback is the title of a merge request whose given title is nothing
// but a draft prefix.
const testFallback = "Update the thing"

// noSlashLines requires that no line of s, once carriage returns are gone as
// GitLab removes them, starts with a slash after spaces or tabs.
func noSlashLines(t *testing.T, s string) {
	t.Helper()

	for line := range strings.SplitSeq(strings.ReplaceAll(s, "\r", ""), "\n") {
		require.False(t, strings.HasPrefix(strings.TrimLeft(line, " \t"), "/"), "a line starts with a slash: %q", line)
	}
}

func TestQuickActionRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "merge", in: "/merge", want: "`/merge`"},
		{name: "assign", in: "/assign @alex", want: "`/assign @alex`"},
		{name: "label", in: "/label ~bug", want: "`/label ~bug`"},
		{name: "approve", in: "/approve", want: "`/approve`"},
		{name: "close", in: "/close", want: "`/close`"},
		{name: "draft", in: "/draft", want: "`/draft`"},
		{name: "ready", in: "/ready", want: "`/ready`"},
		{name: "rebase", in: "/rebase", want: "`/rebase`"},
		{name: "submit review", in: "/submit_review approve", want: "`/submit_review approve`"},
		{name: "a substitution", in: "/shrug", want: "`/shrug`"},
		{name: "upper case, which GitLab reads too", in: "/MERGE", want: "`/MERGE`"},
		{name: "a lone slash", in: "/", want: "`/`"},
		{name: "two slashes", in: "//x", want: "`//x`"},
		{name: "indented", in: "  \t/merge  ", want: "`/merge`"},
		{name: "after text", in: "Raise the threshold.\n/merge\nThanks.", want: "Raise the threshold.\n`/merge`\nThanks."},
		{name: "after a carriage return and a line feed", in: "a\r\n/merge", want: "a\n`/merge`"},
		{name: "a carriage return before the slash, which GitLab deletes", in: "a\n\r/merge", want: "a\n`/merge`"},
		{name: "a lone carriage return", in: "a\r/merge", want: "a/merge"},
		{name: "a line with a backtick inside", in: "/label ~`x`", want: "`` /label ~`x` ``"},
		{name: "a line inside a code fence", in: "```\n/merge\n```", want: "```\n`/merge`\n```"},
		{name: "a path in the middle of a line", in: "see /etc/hosts", want: "see /etc/hosts"},
		{name: "no slash", in: "nothing to do", want: "nothing to do"},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := quickActionRule(tt.in)
			require.Equal(t, tt.want, got)
			noSlashLines(t, got)
		})
	}
}

func TestReferenceRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "a bare issue", in: "Closes #12", want: "Closes `#12`"},
		{name: "a bare merge request", in: "see !34 for more", want: "see `!34` for more"},
		{name: "an issue of another project", in: "fixes group/project#12", want: "fixes `group/project#12`"},
		{name: "an issue in subgroups", in: "Resolves group/sub/project#12.", want: "Resolves `group/sub/project#12`."},
		{name: "a merge request of another project", in: "see group/project!34", want: "see `group/project!34`"},
		{name: "a closing word and a merge request", in: "implements !34", want: "implements `!34`"},
		{name: "the other closing words", in: "Closing #1, fixing #2, resolving #3, implemented #4", want: "Closing `#1`, fixing `#2`, resolving `#3`, implemented `#4`"},
		{name: "a closing word and a URL", in: "Closing https://gitlab.example.com/acme/x/-/issues/7", want: "Closing `https://gitlab.example.com/acme/x/-/issues/7`"},
		{name: "a closing word and a Jira key", in: "Fixes ABC-123", want: "Fixes `ABC-123`"},
		{name: "a closing word with a colon", in: "Closes: #5", want: "Closes: `#5`"},
		{name: "several on a line", in: "!1 !2,!3", want: "`!1` `!2`,`!3`"},
		{name: "at the start of a later line", in: "a\n#9", want: "a\n`#9`"},
		{name: "already in a code span", in: "see `#12` and `group/project!34`", want: "see `#12` and `group/project!34`"},
		{name: "a fragment of a URL", in: "see https://example.com/a/b#12", want: "see https://example.com/a/b#12"},
		{name: "a hash inside a word", in: "C#12", want: "C#12"},
		{name: "a heading", in: "# Title", want: "# Title"},
		{name: "no reference", in: "Raise the threshold to 5.", want: "Raise the threshold to 5."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, referenceRule(tt.in))
		})
	}
}

func TestMergeRequestTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		draft bool
		want  string
	}{
		{name: "a draft", in: "Raise the threshold", draft: true, want: "Draft: Raise the threshold"},
		{name: "not a draft", in: "Raise the threshold", want: "Raise the threshold"},
		{name: "a draft prefix is not doubled", in: "Draft: Raise the threshold", draft: true, want: "Draft: Raise the threshold"},
		{name: "a draft prefix is removed when not a draft", in: "Draft: Raise the threshold", want: "Raise the threshold"},
		{name: "a bracket prefix", in: "[Draft] Raise", want: "Raise"},
		{name: "a parenthesis prefix", in: "(draft) Raise", want: "Raise"},
		{name: "several prefixes in any case", in: "DRAFT: [draft] (Draft) Raise", want: "Raise"},
		{name: "several prefixes and a draft", in: "DRAFT: [draft] Raise", draft: true, want: "Draft: Raise"},
		{name: "a prefix with no title", in: "Draft:", want: testFallback},
		{name: "a word that only starts like a prefix", in: "Drafting rules", want: "Drafting rules"},
		{name: "one line", in: "Raise\nthe\tthreshold", want: "Raise the threshold"},
		{name: "a slash after a draft prefix", in: "Draft: /merge now", want: "`/merge now`"},
		{name: "a slash", in: "/merge now", draft: true, want: "Draft: `/merge now`"},
		{name: "a mention and a reference", in: "Fix @alice, closes #3", want: "Fix `@alice`, closes `#3`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := MergeRequestTitle(tt.in, tt.draft, testFallback)
			require.Equal(t, tt.want, got)
			noSlashLines(t, got)
		})
	}
}

func TestMergeRequestTitleStaysShort(t *testing.T) {
	t.Parallel()

	// The longest title the parameters allow, of nothing but mentions, grows
	// the most when each is put in a code span. GitLab refuses a title over
	// 255 characters.
	const gitlabTitleLimit = 255

	got := MergeRequestTitle(strings.Repeat("@a ", 40), true, testFallback)
	require.LessOrEqual(t, len([]rune(got)), gitlabTitleLimit)
}

func TestCommitTitle(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Raise the threshold", CommitTitle("Raise\nthe threshold"))
	require.Equal(t, "`/merge`", CommitTitle("/merge"))
	require.Equal(t, "Fixes `#3`", CommitTitle("Fixes #3"))
	require.Len(t, CommitTitle(strings.Repeat("x", 200)), MaxCommitTitleBytes)
}

func TestDescriptionText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "quick actions", in: "Why.\n/merge\n/assign @alex", want: "Why.\n`/merge`\n`` /assign `@alex` ``"},
		{name: "references", in: "Closes group/project#12 and !34", want: "Closes `group/project#12` and `!34`"},
		{name: "a mention", in: "ask @alice", want: "ask `@alice`"},
		{name: "an image and raw HTML", in: "![x](https://a.example/p.png) <img src=x>", want: `!\[x](https://a.example/p.png) &lt;img src=x>`},
		{name: "a slash after a line of the HTML rule", in: "<b>\n/merge", want: "&lt;b>\n`/merge`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := descriptionText(tt.in)
			require.Equal(t, tt.want, got)
			noSlashLines(t, got)
		})
	}

	t.Run("it is cut", func(t *testing.T) {
		t.Parallel()

		require.Len(t, descriptionText(strings.Repeat("x", 2*maxDescriptionBytes)), maxDescriptionBytes)
	})
}

func TestCommitMessage(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Raise `#3`\n\nSidekick proposal p-1\n", CommitMessage("Raise #3", "p-1"))
}

func TestDescription(t *testing.T) {
	t.Parallel()

	got := Description("Why.\n/merge", "p-1", "i-1", "https://sidekick.example.com")
	require.Equal(t, "Why.\n`/merge`\n\n---\nOpened by Sidekick for proposal `p-1`. [The investigation](https://sidekick.example.com/investigations/i-1).", got)
	noSlashLines(t, got)

	require.NotContains(t, Description("x", "p-1", "i-1", ""), "investigation")
}
