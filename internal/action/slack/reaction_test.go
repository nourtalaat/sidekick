package slack

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
)

var allowedEmoji = []string{"eyes", "white_check_mark"}

func TestReactionConfigValidate(t *testing.T) {
	t.Parallel()

	good := ReactionConfig{Limits: ReactionLimits{Channels: []string{testChannel}, Emoji: allowedEmoji}}

	tests := []struct {
		name    string
		mutate  func(c *ReactionConfig)
		wantErr string
	}{
		{name: "valid", mutate: func(*ReactionConfig) {}},
		{name: "an emoji with a plus", mutate: func(c *ReactionConfig) { c.Limits.Emoji = []string{"+1"} }},
		{name: "no emoji", mutate: func(c *ReactionConfig) { c.Limits.Emoji = nil }, wantErr: "limits.emoji: list at least one emoji name, such as eyes"},
		{name: "an emoji with colons", mutate: func(c *ReactionConfig) { c.Limits.Emoji = []string{":eyes:"} }, wantErr: `limits.emoji[0]: ":eyes:" is not the name of an emoji without colons, such as eyes`},
		{name: "no channels", mutate: func(c *ReactionConfig) { c.Limits.Channels = nil }, wantErr: "limits.channels: list at least one channel ID, such as C0123456789"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := good
			tt.mutate(&c)

			err := c.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestReactionValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    string
		wantErr   string
		wantLimit bool
	}{
		{name: "an allowed emoji", params: `{"emoji": "eyes"}`},
		{name: "an emoji that is not allowed", params: `{"emoji": "fire"}`, wantErr: `emoji "fire": outside configured limits: the emoji is not one of limits.emoji`, wantLimit: true},
		{name: "colons", params: `{"emoji": ":eyes:"}`, wantErr: "emoji: expected the name of an emoji without colons, such as eyes"},
		{name: "an unknown parameter", params: `{"emoji": "eyes", "channel": "C1"}`, wantErr: `decode the parameters: json: unknown field "channel"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, api := newServer(t)

			err := newReaction(t, api, targetsAt(t, testChannel), allowedEmoji, testChannel).Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
			require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
		})
	}
}

func TestReactionExecute(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	got, err := newReaction(t, api, targetsAt(t, testChannel), allowedEmoji, testChannel).Execute(t.Context(), invocation(`{"emoji": "eyes"}`))
	require.NoError(t, err)
	require.Equal(t, "Added the reaction :eyes: to the alert message.", got.Summary)
	require.Len(t, got.Links, 1)
	require.True(t, f.reactions[testChannel+"/"+testThread+"/eyes"])
}

// TestReactionExecuteTwice is the crash and retry case: the reaction is
// there already, which counts as done.
func TestReactionExecuteTwice(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)
	a := newReaction(t, api, targetsAt(t, testChannel), allowedEmoji, testChannel)

	first, err := a.Execute(t.Context(), invocation(`{"emoji": "eyes"}`))
	require.NoError(t, err)

	second, err := a.Execute(t.Context(), invocation(`{"emoji": "eyes"}`))
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Len(t, f.reactions, 1)
	require.Equal(t, 2, f.calls[methodReactionAdd])
}

func TestReactionExecuteRefusesAChannelOutsideTheLimits(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	_, err := newReaction(t, api, targetsAt(t, "C0000000009"), allowedEmoji, testChannel).Execute(t.Context(), invocation(`{"emoji": "eyes"}`))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Empty(t, f.calls)
}

func TestReactionExecuteRefusesAnEmojiTheLimitsForbidNow(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	_, err := newReaction(t, api, targetsAt(t, testChannel), []string{"white_check_mark"}, testChannel).Execute(t.Context(), invocation(`{"emoji": "eyes"}`))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Empty(t, f.calls)
}

func TestReactionExecuteWithoutASlackAlert(t *testing.T) {
	t.Parallel()

	f, api := newServer(t)

	targets := NewMockTargets(gomock.NewController(t))
	targets.EXPECT().Alert(gomock.Any(), testInvestigationID).Return(Target{}, errors.Join(action.ErrPermanent, errNoAlert))

	_, err := newReaction(t, api, targets, allowedEmoji, testChannel).Execute(t.Context(), invocation(`{"emoji": "eyes"}`))
	require.ErrorIs(t, err, errNoAlert)
	require.True(t, isPermanent(err))
	require.Empty(t, f.calls)
}

func TestReactionExecuteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		code          string
		wantPermanent bool
	}{
		{name: "rate limited", code: "ratelimited"},
		{name: "the message is gone", code: "message_not_found", wantPermanent: true},
		{name: "the bot is not in the channel", code: "not_in_channel", wantPermanent: true},
		{name: "the emoji is unknown to the workspace", code: "invalid_name", wantPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, api := newServer(t)
			f.errorCode = tt.code

			_, err := newReaction(t, api, targetsAt(t, testChannel), allowedEmoji, testChannel).Execute(t.Context(), invocation(`{"emoji": "eyes"}`))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.NotContains(t, err.Error(), testToken)
		})
	}
}

func TestReactionSpecListsTheEmojiAsAnEnum(t *testing.T) {
	t.Parallel()

	_, api := newServer(t)
	spec := newReaction(t, api, targetsAt(t, testChannel), allowedEmoji, testChannel).Spec()

	require.Equal(t, "react", spec.Name)
	require.Contains(t, spec.Description, "Allowed emoji: eyes, white_check_mark.")
	require.JSONEq(t, `{
	  "type": "object", "additionalProperties": false, "required": ["emoji"],
	  "properties": {"emoji": {"type": "string", "enum": ["eyes", "white_check_mark"], "description": "The name of the emoji, without colons."}}
	}`, string(spec.ParamsSchema))
}
