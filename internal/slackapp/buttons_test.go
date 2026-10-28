package slackapp

import (
	"log/slog"
	"net/url"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/require"

	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
)

func TestSlackURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://hooks.slack.com/actions/T01/1/xyz", want: true},
		{url: "https://hooks.slack-gov.com/actions/T01/1/xyz", want: true},
		{url: "https://slack.com/x", want: true},
		{url: "http://127.0.0.1:8080/actions", want: true},
		{url: "http://hooks.slack.com/actions/T01/1/xyz"},
		{url: "https://hooks.slack.com.evil.example/actions"},
		{url: "https://evilslack.com/actions"},
		{url: "https://10.0.0.1/actions"},
		{url: "file:///etc/passwd"},
		{url: ""},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, slackURL(tt.url))
		})
	}
}

func TestDecodeInteraction(t *testing.T) {
	t.Parallel()

	click := `{"type": "block_actions", "user": {"id": "U01ANA"}, "team": {"id": "T01"},
		"actions": [{"type": "button", "block_id": "feedback", "action_id": "verdict_correct", "value": "0192d3a4-0000-7000-8000-0000000000c1"}]}`

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "a button click", body: url.Values{payloadField: {click}}.Encode()},
		{name: "no payload field", body: "other=1", wantErr: true},
		{name: "a payload that is not JSON", body: url.Values{payloadField: {"{"}}.Encode(), wantErr: true},
		{name: "a body that is not a form", body: "%zz", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cb, err := decodeInteraction([]byte(tt.body))
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidPayload)

				return
			}

			require.NoError(t, err)
			require.Equal(t, slack.InteractionTypeBlockActions, cb.Type)
			require.Equal(t, "U01ANA", cb.User.ID)
			require.Len(t, cb.ActionCallback.BlockActions, 1)
			require.Equal(t, slackout.ActionVerdictCorrect, cb.ActionCallback.BlockActions[0].ActionID)
		})
	}
}

// TestHandleInteractionIgnores checks that interactions that are not
// clicks on Sidekick's buttons are acknowledged without doing anything:
// these Buttons have no database, role lookup or HTTP client to use.
func TestHandleInteractionIgnores(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cb   slack.InteractionCallback
	}{
		{name: "a form submission", cb: slack.InteractionCallback{Type: slack.InteractionTypeViewSubmission}},
		{name: "another app's button", cb: clickOn("open_investigation", "0192d3a4-0000-7000-8000-0000000000c1")},
		{name: "a value that is not an investigation ID", cb: clickOn(slackout.ActionStop, "inv_8f2k")},
		{name: "a Stop watching value that is not a watcher ID", cb: clickOn(slackout.ActionStopWatcher+":1", "checkout-health")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := NewButtons(ButtonsOptions{Logger: slog.New(slog.DiscardHandler)})

			resp, err := b.HandleInteraction(t.Context(), tt.cb)
			require.NoError(t, err)
			require.Nil(t, resp, "an empty ack")
		})
	}
}

// clickOn returns a click on the button with actionID and value.
func clickOn(actionID, value string) slack.InteractionCallback {
	return slack.InteractionCallback{
		Type:           slack.InteractionTypeBlockActions,
		User:           slack.User{ID: "U01ANA"},
		ActionCallback: slack.ActionCallbacks{BlockActions: []*slack.BlockAction{{ActionID: actionID, Value: value}}},
	}
}
