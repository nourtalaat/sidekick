package datadogdowntime

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

// markerText is the start of the marker of a proposal. The model's message
// must not hold it, so the marker of a proposal cannot be forged.
const markerText = "[sidekick proposal"

// cleanMessage turns the reason the model wrote into the message of a
// downtime. It applies mute.CleanComment, and also refuses a message with
// an @, because Datadog sends the message of a downtime to the handle that
// follows each @ (a person, a channel or a webhook), and the model must not
// choose who is notified; and a message with the text of a marker. The
// errors wrap action.ErrLimit.
func cleanMessage(raw string) (string, error) {
	text, err := mute.CleanComment(raw)
	if err != nil {
		return "", fmt.Errorf("clean the message: %w", err)
	}

	if strings.Contains(text, "@") {
		return "", fmt.Errorf("the message holds an @; Datadog would notify the handle that follows it, so write the reason without @ handles: %w", action.ErrLimit)
	}

	if strings.Contains(strings.ToLower(text), markerText) {
		return "", fmt.Errorf("the message holds the text %q, which Sidekick keeps for its own marker; write the reason without it: %w", markerText, action.ErrLimit)
	}

	return text, nil
}

// hasMarker reports whether message carries the marker of the proposal as
// its last text: with trailing white space left out, the message ends with
// mute.Marker, and what comes before the marker is white space or nothing.
// mute.HasMarker needs a line break before the marker, but Datadog does not
// say whether it keeps a line break in the message of a downtime, so a line
// break that came back as a space or a tab still counts. cleanMessage keeps
// the model from writing the marker text, so this cannot be forged.
func hasMarker(message, proposalID string) bool {
	trimmed := strings.TrimRightFunc(message, unicode.IsSpace)
	marker := mute.Marker(proposalID)

	if !strings.HasSuffix(trimmed, marker) {
		return false
	}

	before := trimmed[:len(trimmed)-len(marker)]
	if before == "" {
		return true
	}

	r, _ := utf8.DecodeLastRuneInString(before)

	return unicode.IsSpace(r)
}
