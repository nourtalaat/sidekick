package slackcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/slack-go/slack"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/slackapp"
)

const investigateUsage = "Usage: `/sidekick investigate <message link>`, with the link from the message's *Copy link*."

// investigate starts an investigation of a linked message.
func (h *Handler) investigate(ctx context.Context, cmd slack.SlashCommand, args []string) (string, error) {
	if err := h.require(ctx, cmd, roles.Responder); err != nil {
		return "", err
	}

	if len(args) != 1 {
		return "", &userError{msg: investigateUsage}
	}

	err := h.investigator.InvestigateLink(ctx, cmd.TeamID, cmd.APIAppID, eventKeyPrefix+cmd.TriggerID, args[0])

	switch {
	case errors.Is(err, slackapp.ErrBadLink):
		return "", &userError{msg: fmt.Sprintf("%s is not a link to a Slack message. %s", code(args[0]), investigateUsage)}
	case errors.Is(err, slackapp.ErrMessageNotFound):
		return "", &userError{msg: "Sidekick cannot read that message. Invite Sidekick to its channel, then try again."}
	case errors.Is(err, slackapp.ErrOwnMessage):
		return "", &userError{msg: "That is a message from Sidekick itself, so there is nothing to investigate."}
	case err != nil:
		return "", fmt.Errorf("investigate a linked message: %w", err)
	}

	h.log.InfoContext(ctx, "message investigation requested", "user", cmd.UserID)

	return "Sidekick is looking into that message and replies in its thread.", nil
}

// status says what Sidekick runs with and the sender's role.
func (h *Handler) status(ctx context.Context, cmd slack.SlashCommand) (string, error) {
	role, err := h.roles.Role(ctx, cmd.TeamID, cmd.UserID)
	if err != nil {
		return "", fmt.Errorf("look up the role of %s: %w", cmd.UserID, err)
	}

	e := h.rules.Current()

	lines := []string{
		"*Sidekick* " + escape(h.version),
		"Your role: " + string(config.RoleNameOf(role)) + ".",
		fmt.Sprintf("Listeners: %s.", layerCounts(listenerLayers(e))),
		fmt.Sprintf("Routes: %s.", layerCounts(routeLayers(e))),
	}

	if e.LoadedAt.IsZero() {
		lines = append(lines, "Runtime rules have not been loaded yet; only the config file's rules are in effect.")
	} else {
		lines = append(lines, "Runtime rules loaded at "+e.LoadedAt.UTC().Format("2006-01-02 15:04:05")+" UTC.")
	}

	if n := len(e.Shadowed) + len(e.Invalid) + len(e.Disabled); n > 0 {
		lines = append(lines, fmt.Sprintf("%d rules added from Slack are not in effect; `/sidekick listeners` and `/sidekick routes` list them.", n))
	}

	return strings.Join(lines, "\n"), nil
}

func listenerLayers(e *rules.Effective) []rules.Layer {
	out := make([]rules.Layer, len(e.Listeners))
	for i, l := range e.Listeners {
		out[i] = l.Layer
	}

	return out
}

func routeLayers(e *rules.Effective) []rules.Layer {
	out := make([]rules.Layer, len(e.Routes))
	for i, r := range e.Routes {
		out[i] = r.Layer
	}

	return out
}

// layerCounts says how many rules come from each layer, such as "3 (2
// from the config file, 1 added from Slack)".
func layerCounts(layers []rules.Layer) string {
	file := 0

	for _, l := range layers {
		if l == rules.LayerFile {
			file++
		}
	}

	return fmt.Sprintf("%d (%d from the config file, %d added from Slack)", len(layers), file, len(layers)-file)
}
