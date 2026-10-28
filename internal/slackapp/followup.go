package slackapp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/investigator"
	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxQuestionBytes is the longest follow-up question kept, in bytes.
const maxQuestionBytes = 4000

// noteQuestionDenied is what a Slack user below responder sees, only to
// them, when they ask a follow-up question.
const noteQuestionDenied = "Only responders can ask Sidekick follow-up questions, so this one was not sent."

// noteQuestionTriageOnly is what the asker sees, only to them, when they
// ask about a triage-only result.
const noteQuestionTriageOnly = "This was a triage-only result, so there is no investigation to ask about yet, and " +
	"this question was not sent. Press Investigate anyway on the message to start a full investigation, then ask again."

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
	// roles finds who may ask; nil lets everyone ask.
	roles roles.Lookup
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
		roles:    opts.Roles,
	}
}

// followUp adds the thread reply m as a follow-up question to the
// investigation whose Slack message is in m's thread, found through the
// output refs of the Slack outputs. It reports whether the thread is an
// investigation's, so a mention there is not investigated as a new event.
// A reply from a bot, or with no text besides the mention, asks nothing.
// Only a responder or a higher role may ask: anyone else gets a note only
// they see, and the question is not stored. The text is redacted and cut
// to maxQuestionBytes before it is stored, and is never logged.
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

	allowed, err := h.mayAsk(ctx, cmp.Or(m.Team, env.TeamID), m.User)
	if err != nil {
		return true, fmt.Errorf("check who asks: %w", err)
	}

	if !allowed {
		h.refuseQuestion(ctx, id.String(), m)

		return true, nil
	}

	added, err := investigator.AddFollowUp(ctx, f.pool, id, investigator.Question{
		Key:      env.EventID,
		AuthorID: m.User,
		Text:     f.redactor.Text(source.CutText(text, maxQuestionBytes)).Text,
	})
	if errors.Is(err, investigator.ErrTriageOnly) {
		h.tellTriageOnly(ctx, id.String(), m)

		return true, nil
	}

	if err != nil {
		return true, fmt.Errorf("add the follow-up question: %w", err)
	}

	h.log.InfoContext(ctx, "Slack follow-up question", "investigation_id", id.String(), "added", added)

	return true, nil
}

// mayAsk reports whether the Slack user userID in teamID may ask
// follow-up questions: a responder or a higher role, with the role read
// again before a refusal, as the buttons do.
func (h *Handler) mayAsk(ctx context.Context, teamID, userID string) (bool, error) {
	if h.followUps.roles == nil {
		return true, nil
	}

	role, err := responderRole(ctx, h.followUps.roles, teamID, userID)
	if err != nil {
		return false, fmt.Errorf("user %s: %w", userID, err)
	}

	return role.Has(roles.Responder), nil
}

// refuseQuestion tells the author of m, and only them, that their
// question was not sent. A note that cannot be posted is logged; the
// question stays refused either way.
func (h *Handler) refuseQuestion(ctx context.Context, investigationID string, m message) {
	log := h.log.With("investigation_id", investigationID, "slack_user", m.User)
	log.InfoContext(ctx, "Slack follow-up question refused: the user is not a responder")

	if err := h.api.postEphemeral(ctx, m.Channel, m.User, m.ThreadTS, noteQuestionDenied); err != nil {
		log.WarnContext(ctx, "could not tell the user that their follow-up question was refused", "error", err.Error())
	}
}

// tellTriageOnly tells the author of m, and only them, that their question
// was about a triage-only result and was not sent. A note that cannot be
// posted is logged.
func (h *Handler) tellTriageOnly(ctx context.Context, investigationID string, m message) {
	log := h.log.With("investigation_id", investigationID, "slack_user", m.User)
	log.InfoContext(ctx, "Slack follow-up question not sent: the investigation has a triage-only result")

	if err := h.api.postEphemeral(ctx, m.Channel, m.User, m.ThreadTS, noteQuestionTriageOnly); err != nil {
		log.WarnContext(ctx, "could not tell the user that their follow-up question was not sent", "error", err.Error())
	}
}
