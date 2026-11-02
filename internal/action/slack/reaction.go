package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var (
	errBadEmoji   = errors.New("emoji: expected the name of an emoji without colons, such as eyes")
	errEmojiLimit = errors.New("the emoji is not one of limits.emoji")
)

// ReactionConfig is the section of one slack.reaction action.
type ReactionConfig struct {
	Connection `yaml:",inline"`
	Limits     ReactionLimits `yaml:"limits"`
}

// ReactionLimits say where the action may react and with what.
type ReactionLimits struct {
	// Channels are the IDs of the channels whose alert messages may get a
	// reaction. The bot must be a member of each.
	Channels []string `yaml:"channels"`
	// Emoji are the names of the emoji the action may add, without colons.
	Emoji []string `yaml:"emoji"`
}

// Validate checks the section without secrets or network calls.
func (c *ReactionConfig) Validate() error {
	errs := append(c.validate(), validateChannels(c.Limits.Channels)...)

	if len(c.Limits.Emoji) == 0 {
		errs = append(errs, errNoEmoji)
	}

	for i, name := range c.Limits.Emoji {
		if !emojiPattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("limits.emoji[%d]: %q is not the name of an emoji without colons, such as eyes", i, name))
		}
	}

	return errors.Join(errs...)
}

// ReactionParams are what the model fills in for slack.reaction.
type ReactionParams struct {
	Emoji string `json:"emoji"`
}

// Reaction is one configured slack.reaction action.
type Reaction struct {
	service

	emoji []string
}

var _ action.Action = (*Reaction)(nil)

// NewReaction builds a slack.reaction action. It does not call Slack.
func NewReaction(_ context.Context, name string, cfg ReactionConfig, deps registry.Deps) (action.Action, error) {
	svc, err := newService(name, cfg.Connection, cfg.Limits.Channels, deps)
	if err != nil {
		return nil, err
	}

	return &Reaction{service: svc, emoji: cfg.Limits.Emoji}, nil
}

// Spec implements action.Action.
func (r *Reaction) Spec() action.Spec {
	return action.Spec{
		Name: r.name,
		Description: "Adds an emoji reaction to the Slack message that carried the alert. Allowed emoji: " +
			strings.Join(r.emoji, ", ") + ". Allowed channels: " + strings.Join(r.channels, ", ") + ".",
		ParamsSchema: r.schema(),
	}
}

// Validate implements action.Action. The channel is not known until the
// action runs, because it comes from the investigation.
func (r *Reaction) Validate(_ context.Context, raw json.RawMessage) error {
	_, err := r.check(raw)

	return err
}

// Execute implements action.Action. It finds the alert message, checks its
// channel against the limits and adds the reaction; a reaction that is
// there already counts as done, so a repeat adds nothing.
func (r *Reaction) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, err := r.check(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: validate before execute: %w", action.ErrPermanent, err)
	}

	alert, err := r.targets.Alert(ctx, inv.InvestigationID)
	if err != nil {
		return action.Result{}, fmt.Errorf("find the alert message: %w", err)
	}

	if err := r.channelAllowed(alert.Channel); err != nil {
		return action.Result{}, err
	}

	if err := r.client.addReaction(ctx, alert.Channel, alert.TS, p.Emoji); err != nil {
		return action.Result{}, fmt.Errorf("add the reaction: %w", err)
	}

	return r.result(ctx, "Added the reaction :"+p.Emoji+": to the alert message.", alert.Channel, alert.TS), nil
}

// check decodes the parameters and checks them against the limits.
func (r *Reaction) check(raw json.RawMessage) (ReactionParams, error) {
	var p ReactionParams
	if err := decodeStrict(raw, &p); err != nil {
		return ReactionParams{}, err
	}

	if !emojiPattern.MatchString(p.Emoji) {
		return ReactionParams{}, errBadEmoji
	}

	if !slices.Contains(r.emoji, p.Emoji) {
		return ReactionParams{}, fmt.Errorf("emoji %q: %w: %w", p.Emoji, action.ErrLimit, errEmojiLimit)
	}

	return p, nil
}

// schema returns the JSON Schema of ReactionParams, with the allowed emoji
// as an enum.
func (r *Reaction) schema() json.RawMessage {
	names, err := json.Marshal(r.emoji)
	if err != nil {
		names = []byte("[]")
	}

	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["emoji"],
  "properties": {
    "emoji": {"type": "string", "enum": %s, "description": "The name of the emoji, without colons."}
  }
}`, names))
}
