//go:build integration

package publish

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdatesLinkToTheInvestigationPageWithAPublicURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		publicURL string
		want      string
	}{
		{
			name:      "a public URL",
			publicURL: "https://sidekick.example.com/",
			want:      "<https://sidekick.example.com/investigations/" + webhookInvestigation.String() + "|Open in Sidekick>",
		},
		{name: "no public URL"},
		{name: "a public URL that is not http", publicURL: "ftp://sidekick.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			h.handler.publicURL = tt.publicURL

			h.open(t, webhookInvestigation, slackSink)
			require.NoError(t, h.run(t))

			posts := h.slack.callsTo("chat.postMessage")
			require.NotEmpty(t, posts)

			texts := contextTexts(t, posts[0])

			if tt.want == "" {
				require.NotContains(t, texts, "Open in Sidekick")

				return
			}

			require.Contains(t, texts, tt.want)
		})
	}
}
