package slack

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// testToken is the bot token the client tests send.
const testToken = "test-bot-token" //nolint:gosec // A fake value that only the fake server sees.

var errTokenUnreadable = errors.New("the token cannot be read")

func TestClientCall(t *testing.T) {
	t.Parallel()

	ok := fakehttp.Response{Body: []byte(`{"ok":true,"channel":"C123","ts":"1793023321.000200"}`)}
	limited := func(retryAfter string) fakehttp.Response {
		return fakehttp.Response{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": retryAfter}}
	}

	tests := []struct {
		name      string
		responses []fakehttp.Response
		token     error
		want      posted
		wantErr   error
		wantCode  string
		wantWaits []time.Duration
		// wantErrors are the counted errors by error label.
		wantErrors map[string]float64
	}{
		{name: "posted", responses: []fakehttp.Response{ok}, want: posted{Channel: "C123", TS: "1793023321.000200"}},
		{
			name:       "a 429 is retried after Retry-After",
			responses:  []fakehttp.Response{limited("2"), ok},
			want:       posted{Channel: "C123", TS: "1793023321.000200"},
			wantWaits:  []time.Duration{2 * time.Second},
			wantErrors: map[string]float64{errorRateLimited: 1},
		},
		{
			name:       "a 429 without a usable Retry-After waits a second",
			responses:  []fakehttp.Response{limited("soon"), ok},
			want:       posted{Channel: "C123", TS: "1793023321.000200"},
			wantWaits:  []time.Duration{time.Second},
			wantErrors: map[string]float64{errorRateLimited: 1},
		},
		{
			name:       "429 every time",
			responses:  []fakehttp.Response{limited("1"), limited("1"), limited("1"), limited("1")},
			wantErr:    errRateLimited,
			wantWaits:  []time.Duration{time.Second, time.Second, time.Second},
			wantErrors: map[string]float64{errorRateLimited: 4},
		},
		{
			name:       "a Retry-After that is too long",
			responses:  []fakehttp.Response{limited("120")},
			wantErr:    errRateLimited,
			wantErrors: map[string]float64{errorRateLimited: 1},
		},
		{
			name:       "a Slack error",
			responses:  []fakehttp.Response{{Body: []byte(`{"ok":false,"error":"channel_not_found"}`)}},
			wantCode:   "channel_not_found",
			wantErrors: map[string]float64{"channel_not_found": 1},
		},
		{
			name:       "an error code that is not one",
			responses:  []fakehttp.Response{{Body: []byte(`{"ok":false,"error":"Not A Code <b>"}`)}},
			wantCode:   errorOther,
			wantErrors: map[string]float64{errorOther: 1},
		},
		{
			name:       "an HTTP error",
			responses:  []fakehttp.Response{{Status: http.StatusBadGateway}},
			wantErr:    errAny,
			wantErrors: map[string]float64{errorHTTPStatus: 1},
		},
		{
			name:       "a body that is not JSON",
			responses:  []fakehttp.Response{{Body: []byte(`<html>`)}},
			wantErr:    errAny,
			wantErrors: map[string]float64{errorBadResponse: 1},
		},
		{name: "no token", token: errTokenUnreadable, wantErr: errTokenUnreadable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if len(tt.responses) > 0 {
				api.Handle(http.MethodPost, "/api/chat.postMessage", tt.responses...)
			}

			api.ExpectAllCalled()

			reg := prometheus.NewRegistry()
			m, err := newMetrics(reg)
			require.NoError(t, err)

			c, waits := testClient(api, tt.token, m)

			got, err := c.postMessage(t.Context(), message{Channel: "#alerts", Text: "Looking into it"})

			require.Equal(t, tt.wantWaits, waits.all())

			for code, want := range tt.wantErrors {
				require.InDelta(t, want, testutil.ToFloat64(m.errors.WithLabelValues(methodPostMessage, code)), 0, code)
			}

			require.Equal(t, len(tt.wantErrors), testutil.CollectAndCount(m.errors))

			if err != nil {
				require.ErrorContains(t, err, "post message: call "+methodPostMessage, "every error names the method")
			}

			switch {
			case tt.wantCode != "":
				var apiErr *apiError
				require.ErrorAs(t, err, &apiErr)
				require.Equal(t, &apiError{Method: methodPostMessage, Code: tt.wantCode}, apiErr)
			case errors.Is(tt.wantErr, errAny):
				require.Error(t, err)
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestClientSendsTheRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		call     func(ctx context.Context, c *client) error
		method   string
		path     string
		wantBody string
		wantArgs map[string]string
	}{
		{
			name: "chat.update",
			call: func(ctx context.Context, c *client) error {
				_, err := c.update(ctx, message{Channel: "C123", TS: "1.2", Text: "Investigating", Attachments: []attachment{}})

				return err
			},
			method: http.MethodPost,
			path:   "/api/chat.update",
			wantBody: `{"channel":"C123","ts":"1.2","text":"Investigating","attachments":[],
				"link_names":false,"unfurl_links":false,"unfurl_media":false}`,
		},
		{
			name: "chat.getPermalink",
			call: func(ctx context.Context, c *client) error {
				link, err := c.permalink(ctx, "C123", "1.2")
				if link != "https://example.slack.com/archives/C123/p12" {
					return errors.New("unexpected permalink " + link)
				}

				return err
			},
			method:   http.MethodGet,
			path:     "/api/chat.getPermalink",
			wantArgs: map[string]string{"channel": "C123", "message_ts": "1.2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(tt.method, tt.path, fakehttp.Response{
				Body: []byte(`{"ok":true,"channel":"C123","ts":"1.2","permalink":"https://example.slack.com/archives/C123/p12"}`),
			})
			api.ExpectAllCalled()

			c, _ := testClient(api, nil, nil)
			require.NoError(t, tt.call(t.Context(), c))

			requests := api.Requests()
			require.Len(t, requests, 1)
			require.Equal(t, "Bearer "+testToken, requests[0].Header.Get("Authorization"))

			if tt.wantBody != "" {
				require.Equal(t, "application/json; charset=utf-8", requests[0].Header.Get("Content-Type"))
				require.JSONEq(t, tt.wantBody, string(requests[0].Body))
			}

			for k, v := range tt.wantArgs {
				require.Equal(t, v, requests[0].Query.Get(k))
			}
		})
	}
}

func TestClientLimiterPerChannel(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	c := newClient(api.URL(), api.Client(), nil, nil)

	first := c.limiter("C1")
	require.Same(t, first, c.limiter("C1"))
	require.NotSame(t, first, c.limiter("C2"))
	require.Equal(t, channelBurst, first.Burst())
	require.InDelta(t, float64(channelRate), float64(first.Limit()), 0)
}

func TestNewMetricsSharesOneCounter(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	first, err := newMetrics(reg)
	require.NoError(t, err)

	second, err := newMetrics(reg)
	require.NoError(t, err)
	require.Same(t, first.errors, second.errors)
}

// errAny marks a test case that expects some error.
var errAny = errors.New("any error")

// waits records the waits a client asked for.
type waits struct {
	mu   sync.Mutex
	list []time.Duration
}

func (w *waits) wait(_ context.Context, d time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.list = append(w.list, d)

	return nil
}

func (w *waits) all() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.list
}

// testClient returns a client for api that does not sleep, with the token
// testToken, or tokenErr when it is set.
func testClient(api *fakehttp.Server, tokenErr error, m *metrics) (*client, *waits) {
	token := func(context.Context) (string, error) {
		if tokenErr != nil {
			return "", tokenErr
		}

		return testToken, nil
	}

	c := newClient(api.URL()+"/api/", api.Client(), token, m)
	w := &waits{}
	c.wait = w.wait
	c.channelLimit = rate.Inf

	return c, w
}
