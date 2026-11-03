package githubgit

import (
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

const (
	// MaxCommitTitleBytes is the longest first line of the commit message.
	MaxCommitTitleBytes = 72
	// maxPullRequestBodyBytes is the most of a description an action sends;
	// GitHub refuses more than 65536 characters.
	maxPullRequestBodyBytes = 60000
)

// Description returns the body of a pull request: the model's text, made
// safe, and a line that says which proposal it came from, with a link to the
// investigation when publicURL is set.
func Description(body, proposalID, investigationID, publicURL string) string {
	var b strings.Builder

	b.WriteString(source.CutText(gitchange.MarkdownText(body), maxPullRequestBodyBytes))
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "Opened by Sidekick for proposal `%s`.", proposalID)

	if link := weblink.Investigation(publicURL, investigationID); link != "" {
		fmt.Fprintf(&b, " [The investigation](%s).", link)
	}

	return b.String()
}

// CommitMessage returns the message of a commit: the title on one line, and
// the proposal it is for.
func CommitMessage(title, proposalID string) string {
	line := source.CutText(gitchange.OneLine(gitchange.PlainText(title)), MaxCommitTitleBytes)

	return line + "\n\nSidekick proposal " + proposalID + "\n"
}
