package actionhttp

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

const maxTestBytes = 64

func TestDoAndErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		response      fakehttp.Response
		wantBody      string
		wantRetryWait time.Duration
		wantErr       bool
		wantPermanent bool
	}{
		{name: "success", response: fakehttp.Response{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, wantBody: `{"ok":true}`},
		{name: "created", response: fakehttp.Response{Status: http.StatusCreated, Body: []byte(`{}`)}, wantBody: `{}`},
		{
			name:          "rate limited is retried and keeps the wait",
			response:      fakehttp.Response{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "7"}},
			wantRetryWait: 7 * time.Second,
			wantErr:       true,
		},
		{
			name:          "a very long wait is cut",
			response:      fakehttp.Response{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "86400"}},
			wantRetryWait: MaxRetryAfter,
			wantErr:       true,
		},
		{name: "a wait that is not seconds is ignored", response: fakehttp.Response{Status: http.StatusServiceUnavailable, Headers: map[string]string{"Retry-After": "tomorrow"}}, wantErr: true},
		{name: "server error is retried", response: fakehttp.Response{Status: http.StatusBadGateway}, wantErr: true},
		{name: "timeout is retried", response: fakehttp.Response{Status: http.StatusRequestTimeout}, wantErr: true},
		{name: "bad request is final", response: fakehttp.Response{Status: http.StatusBadRequest}, wantErr: true, wantPermanent: true},
		{name: "unauthorized is final", response: fakehttp.Response{Status: http.StatusUnauthorized}, wantErr: true, wantPermanent: true},
		{name: "not found is final", response: fakehttp.Response{Status: http.StatusNotFound}, wantErr: true, wantPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/x", tt.response)

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL()+"/x", http.NoBody)
			require.NoError(t, err)

			got, err := Do(api.Client(), req, maxTestBytes)
			require.NoError(t, err)
			require.Equal(t, tt.wantBody, string(got.Body))
			require.Equal(t, tt.wantRetryWait, got.RetryAfter)

			err = got.Err()
			if !tt.wantErr {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.ErrorAs(t, err, new(*StatusError))
		})
	}
}

func TestDoRefusesALargeAnswer(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, "/x", fakehttp.Response{Status: http.StatusOK, Body: make([]byte, maxTestBytes+1)})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL()+"/x", http.NoBody)
	require.NoError(t, err)

	_, err = Do(api.Client(), req, maxTestBytes)
	require.ErrorIs(t, err, ErrTooLarge)
	require.False(t, isPermanent(err))
}

// failingTransport fails every request, as a service that is down does.
type failingTransport struct{}

var errDown = errors.New("connection refused")

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errDown
}

func TestDoReportsATransportFailure(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://service.example.com/x", http.NoBody)
	require.NoError(t, err)

	_, err = Do(&http.Client{Transport: failingTransport{}}, req, maxTestBytes)
	require.ErrorIs(t, err, errDown)
	require.False(t, isPermanent(err))
}

func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent)
}

func TestCheckURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "https", raw: "https://api.linear.app/graphql"},
		{name: "http on localhost", raw: "http://localhost:8080/x"},
		{name: "http on a loopback address", raw: "http://127.0.0.1:9000"},
		{name: "http on another host", raw: "http://api.example.com", wantErr: "apiURL: must use https; plain http is only allowed for localhost"},
		{name: "a user name", raw: "https://user:pw@api.example.com", wantErr: "apiURL: must not hold a user name or password; use the action's secret instead"}, //nolint:gosec // G101: a made-up URL for the test.
		{name: "no host", raw: "https://", wantErr: "apiURL: must be an absolute https URL"},
		{name: "a relative URL", raw: "/graphql", wantErr: "apiURL: must be an absolute https URL"},
		{name: "another scheme", raw: "ftp://example.com", wantErr: "apiURL: must be an absolute https URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckURL("apiURL", tt.raw)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
		})
	}
}
