package slackapp

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

var errNoToken = errors.New("no token")

func TestThreadParent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []fakehttp.Response
		tokenErr  error
		wantText  string
		wantErr   error
		wantCode  string
	}{
		{
			name:      "the thread's first message",
			responses: []fakehttp.Response{{Body: []byte(`{"ok": true, "messages": [{"ts": "1.1", "text": "parent"}, {"ts": "1.2", "text": "reply"}]}`)}},
			wantText:  "parent",
		},
		{
			name:      "the first message is missing",
			responses: []fakehttp.Response{{Body: []byte(`{"ok": true, "messages": [{"ts": "1.2", "text": "reply"}]}`)}},
			wantErr:   errMessageNotFound,
		},
		{name: "rate limited", responses: []fakehttp.Response{{Status: http.StatusTooManyRequests}}, wantErr: errRateLimited, wantCode: errorRateLimited},
		{name: "an HTTP error", responses: []fakehttp.Response{{Status: http.StatusBadGateway}}, wantCode: errorHTTPStatus},
		{name: "a response that is not JSON", responses: []fakehttp.Response{{Body: []byte("<html>")}}, wantCode: errorBadResponse},
		{name: "a Slack error", responses: []fakehttp.Response{{Body: []byte(`{"ok": false, "error": "channel_not_found"}`)}}, wantCode: "channel_not_found"},
		{name: "a Slack error that is not a code", responses: []fakehttp.Response{{Body: []byte(`{"ok": false, "error": "Not A Code"}`)}}, wantCode: errorOther},
		{name: "a message that does not decode", responses: []fakehttp.Response{{Body: []byte(`{"ok": true, "messages": [{"ts": 7}]}`)}}, wantCode: errorBadResponse},
		{name: "the token cannot be read", tokenErr: errNoToken, wantErr: errNoToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if tt.responses != nil {
				api.Handle(http.MethodGet, "/"+methodReplies, tt.responses...)
			}

			c := newTestWebAPI(t, api, tt.tokenErr)

			m, raw, err := c.threadParent(t.Context(), "C1", "1.1")
			if tt.wantCode != "" {
				require.InDelta(t, 1, testutil.ToFloat64(c.errors.WithLabelValues(methodReplies, tt.wantCode)), 0)
			}

			if tt.wantErr != nil || tt.wantCode != "" {
				require.Error(t, err)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantText, m.Text)
			require.Equal(t, "C1", m.Channel, "the channel is filled in")
			require.JSONEq(t, `{"ts": "1.1", "text": "parent"}`, string(raw))

			req := api.Requests()[0]
			require.Equal(t, "Bearer xoxb-test", req.Header.Get("Authorization"))
			require.Equal(t, "1.1", req.Query.Get("ts"))
			require.Equal(t, "true", req.Query.Get("inclusive"))
		})
	}
}

func TestChannelName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// advance moves the clock between the two lookups.
		advance   time.Duration
		responses []fakehttp.Response
		want      []string
	}{
		{
			name:      "a name is looked up once while it is fresh",
			responses: []fakehttp.Response{{Body: []byte(`{"ok": true, "channel": {"name": "alerts"}}`)}},
			want:      []string{"alerts", "alerts"},
		},
		{
			name:    "a name is looked up again when it is old",
			advance: channelNameTTL + time.Second,
			responses: []fakehttp.Response{
				{Body: []byte(`{"ok": true, "channel": {"name": "alerts"}}`)},
				{Body: []byte(`{"ok": true, "channel": {"name": "alerts-renamed"}}`)},
			},
			want: []string{"alerts", "alerts-renamed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/"+methodInfo, tt.responses...)
			api.ExpectAllCalled()

			c := newTestWebAPI(t, api, nil)
			now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
			c.now = func() time.Time { return now }

			var got []string

			for range tt.want {
				name, err := c.channelName(t.Context(), "C1")
				require.NoError(t, err)

				got = append(got, name)
				now = now.Add(tt.advance)
			}

			require.Equal(t, tt.want, got)
		})
	}
}

// newTestWebAPI returns a client of api whose token is xoxb-test, or
// tokenErr when it is set.
func newTestWebAPI(t *testing.T, api *fakehttp.Server, tokenErr error) *webAPI {
	t.Helper()

	token := func(context.Context) (string, error) { return "xoxb-test", tokenErr }

	c, err := newWebAPI(api.URL()+"/", api.Client(), token, prometheus.NewRegistry())
	require.NoError(t, err)

	return c
}
