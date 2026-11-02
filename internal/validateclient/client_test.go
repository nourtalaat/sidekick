package validateclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// jsonHeader is the header of a JSON answer.
var jsonHeader = map[string]string{"Content-Type": "application/json"}

func TestClientValidate(t *testing.T) {
	t.Parallel()

	req := Request{Format: FormatPrometheusRules, Files: []File{{Path: "rules/a.yaml", Content: "groups: []\n"}}}

	tests := []struct {
		name       string
		req        Request
		answer     *fakehttp.Response
		want       Result
		wantIs     error
		wantErr    string
		wantNoCall bool
	}{
		{
			name:   "files that pass",
			req:    req,
			answer: &fakehttp.Response{Headers: jsonHeader, Body: []byte(`{"ok":true,"errors":[]}`)},
			want:   Result{OK: true, Errors: []Problem{}},
		},
		{
			name:   "files that fail",
			req:    req,
			answer: &fakehttp.Response{Headers: jsonHeader, Body: []byte(`{"ok":false,"errors":[{"path":"rules/a.yaml","line":3,"message":"bad expr"}],"notChecked":[{"path":"b","message":"loki"}],"truncated":true}`)},
			want: Result{
				Errors:     []Problem{{Path: "rules/a.yaml", Line: 3, Message: "bad expr"}},
				NotChecked: []Problem{{Path: "b", Message: "loki"}},
				Truncated:  true,
			},
		},
		{
			name:   "a result with no errors field",
			req:    req,
			answer: &fakehttp.Response{Headers: jsonHeader, Body: []byte(`{"ok":true}`)},
			want:   Result{OK: true, Errors: []Problem{}},
		},
		{
			name:    "a request the validator refuses",
			req:     req,
			answer:  &fakehttp.Response{Status: http.StatusBadRequest, Headers: jsonHeader, Body: []byte(`{"error":"bad request: unknown format"}`)},
			wantIs:  ErrBadRequest,
			wantErr: "unknown format",
		},
		{
			name:    "a busy validator",
			req:     req,
			answer:  &fakehttp.Response{Status: http.StatusServiceUnavailable, Headers: map[string]string{"Retry-After": "5"}, Body: []byte(`{"error":"the validator is busy"}`)},
			wantIs:  ErrBusy,
			wantErr: "the validator is busy",
		},
		{
			name:   "a validation that timed out",
			req:    req,
			answer: &fakehttp.Response{Status: http.StatusGatewayTimeout, Body: []byte(`{"error":"validation timed out after 1m0s"}`)},
			wantIs: ErrTimeout,
		},
		{
			name:    "a failure of the validator",
			req:     req,
			answer:  &fakehttp.Response{Status: http.StatusInternalServerError, Body: []byte("boom")},
			wantIs:  ErrUnavailable,
			wantErr: "status 500: boom",
		},
		{
			name:   "an answer that is not JSON",
			req:    req,
			answer: &fakehttp.Response{Body: []byte("<html>")},
			wantIs: ErrUnavailable,
		},
		{
			name:    "an error body that is long",
			req:     req,
			answer:  &fakehttp.Response{Status: http.StatusInternalServerError, Body: []byte(strings.Repeat("x", 2000))},
			wantIs:  ErrUnavailable,
			wantErr: strings.Repeat("x", maxErrorChars-1) + "…",
		},
		{
			name:   "an answer larger than the client reads",
			req:    req,
			answer: &fakehttp.Response{Body: bytes.Repeat([]byte("z"), maxAnswerBytes+1)},
			wantIs: ErrUnavailable,
		},
		{
			name:       "too many files",
			req:        Request{Format: FormatPrometheusRules, Files: make([]File, MaxFiles+1)},
			wantIs:     ErrTooLarge,
			wantNoCall: true,
		},
		{
			name:       "a request that is too large",
			req:        Request{Format: FormatPrometheusRules, Files: []File{{Path: "a", Content: strings.Repeat("a", MaxRequestBytes)}}},
			wantIs:     ErrTooLarge,
			wantNoCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if tt.answer != nil {
				api.Handle(http.MethodPost, "/validate", *tt.answer)
			}

			api.ExpectAllCalled()

			got, err := New(api.URL()+"/", api.Client()).Validate(t.Context(), tt.req)

			if tt.wantNoCall {
				require.Empty(t, api.Requests())
			}

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)

				if tt.wantErr != "" {
					require.ErrorContains(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)

			sent := api.Requests()[0]
			require.Equal(t, "application/json", sent.Header.Get("Content-Type"))

			var echoed Request
			require.NoError(t, json.Unmarshal(sent.Body, &echoed))
			require.Equal(t, tt.req, echoed)
		})
	}
}

func TestClientValidateUnreachable(t *testing.T) {
	t.Parallel()

	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()

	_, err := New(url, nil).Validate(t.Context(), Request{Format: FormatTerraform, Files: []File{{Path: "main.tf"}}})
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestClientValidateContext(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, "/validate", fakehttp.Response{Body: []byte(`{"ok":true}`)})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := New(api.URL(), api.Client()).Validate(ctx, Request{Format: FormatTerraform, Files: []File{{Path: "main.tf"}}})
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrUnavailable)
}
