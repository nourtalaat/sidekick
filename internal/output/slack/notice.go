package slack

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/output"
)

// noticeStyle is how a notice looks: the warning yellow and its emoji,
// the status style of a budget near or at its cap.
var noticeStyle = verdictStyle{emoji: ":warning:", color: colorFailed}

var _ output.Noticer = (*Sink)(nil)

// Notice posts n as a new message in the configured channel, not in a
// thread, since it belongs to no one investigation.
func (s *Sink) Notice(ctx context.Context, n output.Notice) error {
	v := noticeView(n)

	if _, err := s.client.postMessage(ctx, message{Channel: s.channel, Text: v.text, Attachments: []attachment{v.attachment}}); err != nil {
		return fmt.Errorf("post the %s notice: %w", n.Kind, err)
	}

	return nil
}

// noticeView renders a notice: the emoji and the bold title, then the
// text, all escaped.
func noticeView(n output.Notice) view {
	title := escape(cut(n.Title, maxTitleChars))
	text := escape(cut(n.Text, maxReasonChars))

	return view{
		text:       title + ": " + text,
		attachment: attachment{Color: noticeStyle.color, Blocks: []block{section(noticeStyle.emoji + " *" + title + "*\n" + text)}},
	}
}
