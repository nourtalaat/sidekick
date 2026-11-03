package slack

import (
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// unknown stands for a revision fact that the platform does not say.
const unknown = "unknown"

// maxRevisionNameChars is the longest name of a revision in a heading or a
// dialog, in characters. The code block under the heading holds the whole
// text.
const maxRevisionNameChars = 60

// revisionTimeLayout writes a deploy time in a code block, where a Slack
// date token would not render.
const revisionTimeLayout = "2006-01-02 15:04 UTC"

// revisionBlocks renders the two revisions of a rollback in place of the
// field list: a heading, with verb as its first words, and one code block
// for each chunk of text that fits in a Slack section. It reports whether
// the revisions are shown whole; when not, it returns a warning and false,
// because a person must never decide on text that was cut.
func revisionBlocks(verb string, r action.Revisions) ([]block, bool) {
	text := revisionText(r)

	if escapedLen(text) > maxParamsChars {
		return []block{section(":warning: The revisions are too long to show here. Open the investigation in Sidekick and read them before you decide.")}, false
	}

	heading := fmt.Sprintf("*%s %s to %s*", verb, escape(revisionName(r.Running)), escape(revisionName(r.GoesBackTo)))
	blocks := []block{section(heading)}

	for _, chunk := range chunksOf(text, maxParamsChunk) {
		blocks = append(blocks, section("```"+chunk+"```"))
	}

	return blocks, true
}

// revisionText writes the two revisions, one after the other, as
//
//	Running now
//	  revision:    ReplicaSet revision 7
//	  version:     checkout:1.4.7
//	  deployed at: 2026-11-03 09:30 UTC
//	  deployed by: unknown
//
// The values are text that Display spelled out, so nothing in them can
// close the code block. The caller escapes the whole text.
func revisionText(r action.Revisions) string {
	return "Running now\n" + revisionLines(r.Running) + "\n\nGoes back to\n" + revisionLines(r.GoesBackTo)
}

// revisionLines writes one revision, with "unknown" for what is missing.
// The notes are left out when there are none.
func revisionLines(r action.Revision) string {
	lines := []string{
		revisionLine("revision", r.Kind+" "+r.ID),
		revisionLine("version", orUnknown(r.Version)),
		revisionLine("deployed at", deployedAt(r)),
		revisionLine("deployed by", orUnknown(r.DeployedBy)),
	}

	if r.Notes != "" {
		lines = append(lines, revisionLine("notes", r.Notes))
	}

	return strings.Join(lines, "\n")
}

// revisionLine is one line of a revision, with its label padded so the
// values line up.
func revisionLine(label, value string) string {
	const width = len("deployed at:")

	return fmt.Sprintf("  %-*s %s", width, label+":", value)
}

// revisionName names a revision in a heading or a dialog: its version, or
// when it has none its kind and ID, cut when long.
func revisionName(r action.Revision) string {
	if r.Version != "" {
		return cut(r.Version, maxRevisionNameChars)
	}

	return cut(r.Kind+" "+r.ID, maxRevisionNameChars)
}

// deployedAt is the deploy time in UTC, or "unknown".
func deployedAt(r action.Revision) string {
	if r.DeployedAt == nil {
		return unknown
	}

	return r.DeployedAt.UTC().Format(revisionTimeLayout)
}

// orUnknown returns s, or "unknown" when it is empty.
func orUnknown(s string) string {
	if s == "" {
		return unknown
	}

	return s
}
