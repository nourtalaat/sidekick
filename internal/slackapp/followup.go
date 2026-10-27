package slackapp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/investigator"
	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxQuestionBytes is the longest follow-up question kept, in bytes.
const maxQuestionBytes = 4000

// followUps is what turns thread replies into follow-up questions.
type followUps struct {
	// anyReply takes every reply a person writes in an investigation's
	// thread, not only those that mention Sidekick.
	anyReply bool
	// sinks are the Slack outputs, whose messages mark the threads of
	// investigations.
	sinks    []string
	pool     *pgxpool.Pool
	redactor *redact.Redactor
}

// newFollowUps reads the follow-up setting and the Slack outputs of
// opts.Config.
func newFollowUps(opts Options) followUps {
	var sinks []string

	for _, name := range slices.Sorted(maps.Keys(opts.Config.Outputs)) {
		if opts.Config.Outputs[name].Type == slackout.Type {
			sinks = append(sinks, name)
		}
	}

	return followUps{
		anyReply: opts.Config.Slack.FollowUps == config.SlackFollowUpsAny,
		sinks:    sinks,
		pool:     opts.Pool,
		redactor: opts.Redactor,
	}
}

// followUp adds the thread reply m as a follow-up question to the
// investigation whose Slack message is in m's thread, found through the
// output refs of the Slack outputs. It reports whether the thread is an
// investigation's, so a mention there is not investigated as a new event.
// A reply from a bot, or with no text besides the mention, asks nothing.
// The text is redacted and cut to maxQuestionBytes before it is stored,
// and is never logged.
func (h *Handler) followUp(ctx context.Context, env envelope, m message) (bool, error) {
	f := h.followUps

	id, found, err := investigator.FindInvestigation(ctx, f.pool, f.sinks, slackout.External(m.Channel, m.ThreadTS))
	if err != nil {
		return false, fmt.Errorf("find the investigation of the thread: %w", err)
	}

	if !found {
		return false, nil
	}

	text := strings.TrimSpace(strings.ReplaceAll(m.flatText(), mentionOf(env.botUserID()), ""))
	if m.isBot() || text == "" {
		return true, nil
	}

	// TODO: [SK-18] Check the Slack user's role here: only a responder or a
	// higher role may ask follow-up questions.
	added, err := investigator.AddFollowUp(ctx, f.pool, id, investigator.Question{
		Key:      env.EventID,
		AuthorID: m.User,
		Text:     f.redactor.Text(source.CutText(text, maxQuestionBytes)).Text,
	})
	if err != nil {
		return true, fmt.Errorf("add the follow-up question: %w", err)
	}

	h.log.InfoContext(ctx, "Slack follow-up question", "investigation_id", id.String(), "added", added)

	return true, nil
}
