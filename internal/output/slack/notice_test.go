package slack

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output"
)

func TestNotice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		notice   output.Notice
		response fakehttp.Response
		wantText string
		wantErr  bool
	}{
		{
			name: "posts in the channel, not in a thread",
			notice: output.Notice{Kind: output.NoticeDailyCap, Title: "Daily cap reached",
				Text: "Route payments has used all of today's $40.00 cap."},
			response: postedResponse(mainTS),
			wantText: "Daily cap reached: Route payments has used all of today's $40.00 cap.",
		},
		{
			name:     "escapes the text",
			notice:   output.Notice{Kind: output.NoticeRateLimit, Title: "Rate limit <reached>", Text: "Route a&b <!channel>"},
			response: postedResponse(mainTS),
			wantText: "Rate limit &lt;reached&gt;: Route a&amp;b &lt;!channel&gt;",
		},
		{
			name:     "a Slack error is returned",
			notice:   output.Notice{Kind: output.NoticeDailyCap, Title: "Daily cap reached", Text: "Text."},
			response: fakehttp.Response{Body: []byte(`{"ok":false,"error":"channel_not_found"}`)},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, pathPost, tt.response)
			api.ExpectAllCalled()

			s, _ := testSink(t, api)

			err := s.Notice(t.Context(), tt.notice)
			if tt.wantErr {
				var apiErr *apiError
				require.ErrorAs(t, err, &apiErr)
				require.Equal(t, "channel_not_found", apiErr.Code)

				return
			}

			require.NoError(t, err)

			sent := decodeMessage(t, api.Requests()[0].Body)
			require.Equal(t, "#alerts", sent.Channel)
			require.Empty(t, sent.ThreadTS)
			require.Equal(t, tt.wantText, sent.Text)
			require.Equal(t, colorFailed, sent.Attachments[0].Color)
		})
	}
}
