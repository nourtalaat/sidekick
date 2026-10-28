package slackapp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/config"
	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
)

// TestAlertBlocks returns the blocks of the sample alert that `sidekick
// test-alert --channel` posts: one section with text, whose block ID is
// the marker that makes the app take its own message as an alert.
func TestAlertBlocks(text string) []map[string]any {
	return []map[string]any{{
		"type":     "section",
		"block_id": slackout.TestAlertBlockID,
		"text":     map[string]string{"type": "mrkdwn", "text": text},
	}}
}

// TestAlertListener returns the name of the listener that would take the
// sample alert, or "" when none would. posted is the message object of the
// chat.postMessage answer, and channelName and channelID are the channel's
// name, with or without "#", and its ID. A listener has to match the
// channel, the sender and the text like it does for any other message.
func TestAlertListener(listeners map[string]config.Listener, channelName, channelID string, posted json.RawMessage) (string, error) {
	compiled, err := compileListeners(listeners)
	if err != nil {
		return "", fmt.Errorf("compile the listeners: %w", err)
	}

	var m message
	if err := json.Unmarshal(posted, &m); err != nil {
		return "", fmt.Errorf("decode the posted message: %w", err)
	}

	m.Channel = channelID
	text := m.flatText()
	name := strings.TrimPrefix(channelName, "#")

	for _, l := range compiled {
		if l.matches(m, text) && l.matchesChannel(channelID, name) {
			return l.Name, nil
		}
	}

	return "", nil
}
