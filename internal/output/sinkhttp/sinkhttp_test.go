package sinkhttp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// The output type and operation of the test calls.
const (
	testType      = "test"
	testOperation = "create"
	testPath      = "/comments"
)

func TestDo(t *testing.T) {
	t.Parallel()

	tooMany := func(after string) fakehttp.Response {
		return fakehttp.Response{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": after}}
	}

	created := fakehttp.Response{Status: http.StatusCreated, Body: []byte(`{"id":"1"}`)}

	tests := []struct {
		name       string
		responses  []fakehttp.Response
		wantStatus int
		wantBody   string
		wantErr    error
		wantWaits  []time.Duration
		wantErrors map[string]float64
	}{
		{name: "a created resource", responses: []fakehttp.Response{created}, wantStatus: http.StatusCreated, wantBody: `{"id":"1"}`},
		{
			name:       "a server error is returned, not tried again",
			responses:  []fakehttp.Response{{Status: http.StatusServiceUnavailable, Body: []byte("down")}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "down",
			wantErrors: map[string]float64{"http_503": 1},
		},
		{
			name:       "a 429 waits for Retry-After and tries again",
			responses:  []fakehttp.Response{tooMany("2"), created},
			wantStatus: http.StatusCreated,
			wantBody:   `{"id":"1"}`,
			wantWaits:  []time.Duration{2 * time.Second},
			wantErrors: map[string]float64{reasonRateLimited: 1},
		},
		{
			name:       "a 429 without a usable Retry-After waits a second",
			responses:  []fakehttp.Response{tooMany("soon"), created},
			wantStatus: http.StatusCreated,
			wantBody:   `{"id":"1"}`,
			wantWaits:  []time.Duration{defaultRetryAfter},
			wantErrors: map[string]float64{reasonRateLimited: 1},
		},
		{
			name:       "429 after every retry",
			responses:  []fakehttp.Response{tooMany("1"), tooMany("1"), tooMany("1"), tooMany("1")},
			wantErr:    ErrRateLimited,
			wantWaits:  []time.Duration{time.Second, time.Second, time.Second},
			wantErrors: map[string]float64{reasonRateLimited: 4},
		},
		{
			name:       "a Retry-After longer than a minute fails at once",
			responses:  []fakehttp.Response{tooMany("120")},
			wantErr:    ErrRateLimited,
			wantErrors: map[string]float64{reasonRateLimited: 1},
		},
		{
			name:       "a body over the limit",
			responses:  []fakehttp.Response{{Body: bytes.Repeat([]byte("a"), maxResponseBytes+1)}},
			wantErr:    errTooLarge,
			wantErrors: map[string]float64{reasonRequestFailed: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, testPath, tt.responses...)
			api.ExpectAllCalled()

			c, waits := testClient(t, api.Client())

			resp, err := c.Do(t.Context(), testOperation, postTo(api.URL()+testPath))
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantStatus, resp.Status)
				require.Equal(t, tt.wantBody, string(resp.Body))
			}

			require.Equal(t, tt.wantWaits, waits.all())

			for reason, want := range tt.wantErrors {
				require.InDelta(t, want, testutil.ToFloat64(c.errors.WithLabelValues(testType, testOperation, reason)), 0, reason)
			}

			require.Equal(t, len(tt.wantErrors), testutil.CollectAndCount(c.errors))
		})
	}
}

func TestDoRequestFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newRequest func(url string) RequestFunc
		wantReason string
	}{
		{
			name: "the request cannot be made",
			newRequest: func(string) RequestFunc {
				return func(ctx context.Context) (*http.Request, error) {
					return http.NewRequestWithContext(ctx, http.MethodPost, "://bad", nil)
				}
			},
			wantReason: reasonRequestFailed,
		},
		{
			name: "nothing listens at the address",
			newRequest: func(string) RequestFunc {
				return postTo("http://127.0.0.1:1/comments")
			},
			wantReason: reasonRequestFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, _ := testClient(t, http.DefaultClient)

			_, err := c.Do(t.Context(), testOperation, tt.newRequest(""))
			require.Error(t, err)
			require.InDelta(t, 1, testutil.ToFloat64(c.errors.WithLabelValues(testType, testOperation, tt.wantReason)), 0)
		})
	}
}

func TestResponseErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "200", status: http.StatusOK},
		{name: "204", status: http.StatusNoContent},
		{name: "301", status: http.StatusMovedPermanently, wantErr: true},
		{name: "404", status: http.StatusNotFound, wantErr: true},
		{name: "500", status: http.StatusInternalServerError, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := Response{Status: tt.status}.Err()
			if !tt.wantErr {
				require.NoError(t, err)

				return
			}

			statusErr, ok := errors.AsType[*StatusError](err)
			require.True(t, ok)
			require.Equal(t, tt.status, statusErr.Status)
		})
	}
}

func TestStatusReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		want   string
	}{
		{name: "a known status", status: http.StatusBadGateway, want: "http_502"},
		{name: "below 100", status: 42, want: reasonHTTPOther},
		{name: "above 599", status: 999, want: reasonHTTPOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, statusReason(tt.status))
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "seconds", value: "7", want: 7 * time.Second},
		{name: "zero", value: "0", want: 0},
		{name: "an HTTP date", value: now.Add(30 * time.Second).Format(http.TimeFormat), want: 30 * time.Second},
		{name: "a date in the past", value: now.Add(-time.Hour).Format(http.TimeFormat), want: 0},
		{name: "missing", value: "", want: defaultRetryAfter},
		{name: "negative", value: "-3", want: defaultRetryAfter},
		{name: "garbage", value: "soon", want: defaultRetryAfter},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, parseRetryAfter(tt.value, now))
		})
	}
}

func TestNewSharesTheCounter(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	first, err := New("linear.comment", nil, reg)
	require.NoError(t, err)

	second, err := New("jira.comment", nil, reg)
	require.NoError(t, err)

	first.Failed(testOperation, ReasonBadResponse)
	second.Failed(testOperation, ReasonAPIError)

	require.Equal(t, 2, testutil.CollectAndCount(first.errors))
	require.NotNil(t, first.http)
}

// waits records the waits of a test client instead of sleeping.
type waits struct {
	mu sync.Mutex
	d  []time.Duration
}

// all returns the recorded waits.
func (w *waits) all() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.d
}

// testClient returns a client that records its waits, with metrics in a
// registry of its own.
func testClient(t *testing.T, hc *http.Client) (*Client, *waits) {
	t.Helper()

	c, err := New(testType, hc, prometheus.NewRegistry())
	require.NoError(t, err)

	w := &waits{}
	c.wait = func(_ context.Context, d time.Duration) error {
		w.mu.Lock()
		defer w.mu.Unlock()

		w.d = append(w.d, d)

		return nil
	}

	return c, w
}

// postTo returns a RequestFunc that posts a small JSON body to url.
func postTo(url string) RequestFunc {
	return func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"body":"hi"}`))
	}
}
