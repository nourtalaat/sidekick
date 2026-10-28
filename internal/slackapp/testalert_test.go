package slackapp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
)

func TestIsTestAlert(t *testing.T) {
	t.Parallel()

	marked := json.RawMessage(`[{"type": "section", "block_id": "` + slackout.TestAlertBlockID + `", "text": {"type": "mrkdwn", "text": "x"}}]`)
	second := json.RawMessage(`[{"type": "section", "block_id": "other"}, {"type": "section", "block_id": "` + slackout.TestAlertBlockID + `"}]`)
	plain := json.RawMessage(`[{"type": "section", "block_id": "feedback"}]`)

	tests := []struct {
		name  string
		msg   message
		appID string
		want  bool
	}{
		{name: "posted by this app with the marker", msg: message{AppID: "A1", Blocks: marked}, appID: "A1", want: true},
		{name: "posted by another app with the marker", msg: message{AppID: "A2", Blocks: marked}, appID: "A1"},
		{name: "posted by a person with the marker", msg: message{User: "U1", Blocks: marked}, appID: "A1"},
		{name: "posted by this app without the marker", msg: message{AppID: "A1", Blocks: plain}, appID: "A1"},
		{name: "the marker is on a later block", msg: message{AppID: "A1", Blocks: second}, appID: "A1"},
		{name: "posted by this app without blocks", msg: message{AppID: "A1"}, appID: "A1"},
		{name: "blocks that are not a list", msg: message{AppID: "A1", Blocks: json.RawMessage(`{"block_id": "x"}`)}, appID: "A1"},
		{name: "the app's own ID is not known", msg: message{Blocks: marked}, appID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.msg.isTestAlert(tt.appID))
		})
	}
}

func TestTestAlertListener(t *testing.T) {
	t.Parallel()

	posted := func(profileName string) json.RawMessage {
		blocks, err := json.Marshal(TestAlertBlocks("Pod default/api is restarting"))
		require.NoError(t, err)

		return json.RawMessage(`{"type": "message", "subtype": "bot_message", "bot_id": "B9", "app_id": "A1",
			"bot_profile": {"id": "B9", "app_id": "A1", "name": "` + profileName + `"},
			"text": "Pod default/api is restarting", "blocks": ` + string(blocks) + `}`)
	}

	tests := []struct {
		name      string
		listeners map[string]config.Listener
		channel   string
		want      string
	}{
		{
			name:      "a listener on the channel by name that takes bots",
			listeners: map[string]config.Listener{"alerts": {Channel: "#alerts", Match: config.ListenerMatch{Sender: config.ListenerSender{Kind: "bot"}}}},
			channel:   "#alerts",
			want:      "alerts",
		},
		{
			name:      "a listener on the channel by ID",
			listeners: map[string]config.Listener{"alerts": {Channel: "C01ALERTS"}},
			channel:   "alerts",
			want:      "alerts",
		},
		{
			name:      "a listener on another channel",
			listeners: map[string]config.Listener{"alerts": {Channel: "#ops"}},
			channel:   "#alerts",
		},
		{
			name:      "a listener for other bots",
			listeners: map[string]config.Listener{"alerts": {Channel: "#alerts", Match: config.ListenerMatch{Sender: config.ListenerSender{Names: []string{"SigNoz"}}}}},
			channel:   "#alerts",
		},
		{
			name:      "a listener for people",
			listeners: map[string]config.Listener{"alerts": {Channel: "#alerts", Match: config.ListenerMatch{Sender: config.ListenerSender{Kind: "human"}}}},
			channel:   "#alerts",
		},
		{
			name:      "a listener whose text pattern does not match",
			listeners: map[string]config.Listener{"alerts": {Channel: "#alerts", Match: config.ListenerMatch{Text: "resolved"}}},
			channel:   "#alerts",
		},
		{
			name:      "a listener whose text pattern matches the block text",
			listeners: map[string]config.Listener{"alerts": {Channel: "#alerts", Match: config.ListenerMatch{Text: "restarting"}}},
			channel:   "#alerts",
			want:      "alerts",
		},
		{name: "no listeners", channel: "#alerts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := TestAlertListener(tt.listeners, tt.channel, "C01ALERTS", posted("Sidekick"))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	t.Run("a bot name that the listener names", func(t *testing.T) {
		t.Parallel()

		listeners := map[string]config.Listener{"alerts": {Channel: "#alerts", Match: config.ListenerMatch{Sender: config.ListenerSender{Names: []string{"sidekick"}}}}}

		got, err := TestAlertListener(listeners, "#alerts", "C01ALERTS", posted("Sidekick"))
		require.NoError(t, err)
		require.Equal(t, "alerts", got)
	})

	t.Run("a message that is not JSON", func(t *testing.T) {
		t.Parallel()

		_, err := TestAlertListener(nil, "#alerts", "C01ALERTS", json.RawMessage(`nope`))
		require.ErrorContains(t, err, "decode the posted message")
	})
}
