package slackapp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
)

// The reasons InvestigateLink cannot investigate a message.
var (
	// ErrBadLink means the text is not a link to a Slack message.
	ErrBadLink = errors.New("not a link to a Slack message")
	// ErrMessageNotFound means Slack has no such message, or Sidekick is
	// not in its channel.
	ErrMessageNotFound = errors.New("the message was not found, or Sidekick is not in its channel")
	// ErrOwnMessage means the link is to a message Sidekick posted.
	ErrOwnMessage = errors.New("the message is one Sidekick posted")
)

// archivePath is the path of a message link: /archives/<channel>/p<ts>,
// where ts is the message timestamp without its dot.
var archivePath = regexp.MustCompile(`^/archives/([CDG][A-Z0-9]{2,})/p([0-9]{10})([0-9]{6})/?$`)

// threadTSPattern is a message timestamp such as 1700000000.123456.
var threadTSPattern = regexp.MustCompile(`^[0-9]{10}\.[0-9]{6}$`)

// slackLinkHosts are the ends of the hosts of Slack message links, such
// as acme.slack.com, acme.enterprise.slack.com or, on GovSlack,
// acme.slack-gov.com.
var slackLinkHosts = []string{".slack.com", ".slack-gov.com"}

// Slack error codes that mean the message cannot be read.
var notFoundCodes = []string{"channel_not_found", "not_in_channel", "thread_not_found", "message_not_found"}

// MessageLink is a parsed link to a Slack message.
type MessageLink struct {
	Channel string
	// TS is the message's timestamp, or the thread's first message's for a
	// link to a reply in a thread.
	TS string
}

// InvestigateLink stores a mention event for the message link points to,
// so it is investigated as if someone had mentioned Sidekick in reply to
// it. For a link to a reply in a thread, that is the thread's first
// message. appID is the app's ID, from the command, so Sidekick never
// investigates its own messages; key is the event key, unique to the
// request, so a request Slack sends again is stored once. The caller
// bounds how long it may take.
func (h *Handler) InvestigateLink(ctx context.Context, teamID, appID, key, link string) error {
	l, err := ParseMessageLink(link)
	if err != nil {
		return fmt.Errorf("investigate a linked message: %w", err)
	}

	parent, raw, err := h.api.threadParent(ctx, l.Channel, l.TS)
	if err != nil {
		var apiErr *apiError
		if errors.Is(err, errMessageNotFound) || (errors.As(err, &apiErr) && slices.Contains(notFoundCodes, apiErr.Code)) {
			return fmt.Errorf("%w: %w", ErrMessageNotFound, err)
		}

		return fmt.Errorf("read the linked message: %w", err)
	}

	if parent.fromApp(appID, "") {
		return ErrOwnMessage
	}

	text := parent.flatText()

	match, err := h.firstListener(ctx, parent, text)
	if err != nil {
		return fmt.Errorf("investigate a linked message: %w", err)
	}

	ev := buildEvent(key, teamID, parent, text, match, raw)
	ev.Kind = event.KindMention

	if err := h.store(ctx, config.SlackMentionIntake, false, ev); err != nil {
		return fmt.Errorf("investigate a linked message: %w", err)
	}

	return nil
}

// ParseMessageLink reads a Slack message link, as "Copy link" gives it:
// https://<workspace>.slack.com/archives/<channel>/p<ts>, with a
// thread_ts query for a reply in a thread. Slack may send it inside "<"
// and ">".
func ParseMessageLink(link string) (MessageLink, error) {
	link = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(link), "<"), ">")
	link, _, _ = strings.Cut(link, "|")

	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || !slices.ContainsFunc(slackLinkHosts, func(h string) bool { return strings.HasSuffix(u.Hostname(), h) }) {
		return MessageLink{}, ErrBadLink
	}

	m := archivePath.FindStringSubmatch(u.Path)
	if m == nil {
		return MessageLink{}, ErrBadLink
	}

	l := MessageLink{Channel: m[1], TS: m[2] + "." + m[3]}

	if thread := u.Query().Get("thread_ts"); thread != "" {
		if !threadTSPattern.MatchString(thread) {
			return MessageLink{}, ErrBadLink
		}

		l.TS = thread
	}

	return l, nil
}
