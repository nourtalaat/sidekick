package mute

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
)

// MaxCommentRunes is the longest reason a mute may carry, not counting the
// marker.
const MaxCommentRunes = 500

// markerPrefix starts the marker line.
const markerPrefix = "[sidekick proposal "

// CleanComment turns the reason the model wrote into text for a mute: runs
// of whitespace, line breaks included, become one space. It refuses an empty
// text, one longer than MaxCommentRunes, and one with control or format
// characters (such as a right-to-left override, which can disguise text).
// The errors wrap action.ErrLimit. Because the result holds no line break,
// the marker that WithMarker adds on a line of its own cannot be forged by
// the model.
func CleanComment(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("the comment is not valid UTF-8 text: %w", action.ErrLimit)
	}

	for _, r := range raw {
		if (unicode.IsControl(r) && !unicode.IsSpace(r)) || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("the comment holds the control or format character U+%04X: %w", r, action.ErrLimit)
		}
	}

	text := strings.Join(strings.Fields(raw), " ")

	switch n := utf8.RuneCountInString(text); {
	case n == 0:
		return "", fmt.Errorf("the comment is empty; give the reason for the mute: %w", action.ErrLimit)
	case n > MaxCommentRunes:
		return "", fmt.Errorf("the comment has %d characters, and the most is %d: %w", n, MaxCommentRunes, action.ErrLimit)
	default:
		return text, nil
	}
}

// Marker returns the line that marks a mute as made by the proposal with
// proposalID: "[sidekick proposal <id>]".
func Marker(proposalID string) string {
	return markerPrefix + proposalID + "]"
}

// WithMarker returns comment followed, on a line of its own, by the marker
// of the proposal.
func WithMarker(comment, proposalID string) string {
	return comment + "\n" + Marker(proposalID)
}

// HasMarker reports whether comment ends with the marker line of the
// proposal, as WithMarker writes it.
func HasMarker(comment, proposalID string) bool {
	return strings.HasSuffix(comment, "\n"+Marker(proposalID))
}
