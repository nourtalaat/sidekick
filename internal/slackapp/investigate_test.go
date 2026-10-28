package slackapp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMessageLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		link string
		want MessageLink
	}{
		{
			name: "a top-level message",
			link: "https://acme.slack.com/archives/C01ALERTS/p1761572520123456",
			want: MessageLink{Channel: "C01ALERTS", TS: "1761572520.123456"},
		},
		{
			name: "a reply in a thread",
			link: "https://acme.slack.com/archives/C01ALERTS/p1761572600000100?thread_ts=1761572520.123456&cid=C01ALERTS",
			want: MessageLink{Channel: "C01ALERTS", TS: "1761572520.123456"},
		},
		{
			name: "inside angle brackets, as Slack sends links",
			link: "<https://acme.enterprise.slack.com/archives/G01OPS/p1761572520123456>",
			want: MessageLink{Channel: "G01OPS", TS: "1761572520.123456"},
		},
		{
			name: "a GovSlack link",
			link: "https://agency.slack-gov.com/archives/C01ALERTS/p1761572520123456",
			want: MessageLink{Channel: "C01ALERTS", TS: "1761572520.123456"},
		},
		{name: "not a link", link: "the checkout alert"},
		{name: "plain http", link: "http://acme.slack.com/archives/C01ALERTS/p1761572520123456"},
		{name: "another host", link: "https://slack.com.example.org/archives/C01ALERTS/p1761572520123456"},
		{name: "a channel link", link: "https://acme.slack.com/archives/C01ALERTS"},
		{name: "a short timestamp", link: "https://acme.slack.com/archives/C01ALERTS/p17615725"},
		{name: "a bad thread timestamp", link: "https://acme.slack.com/archives/C01ALERTS/p1761572520123456?thread_ts=now"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseMessageLink(tt.link)
			if tt.want == (MessageLink{}) {
				require.ErrorIs(t, err, ErrBadLink)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
