package model

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetryAfter(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)

	tests := []struct {
		name    string
		headers map[string]string
		wantMin time.Duration
		wantMax time.Duration
	}{
		{name: "no headers"},
		{name: "seconds", headers: map[string]string{RetryAfterHeader: "12"}, wantMin: 12 * time.Second, wantMax: 12 * time.Second},
		{name: "fractional seconds", headers: map[string]string{RetryAfterHeader: "1.5"}, wantMin: 1500 * time.Millisecond, wantMax: 1500 * time.Millisecond},
		{
			name:    "milliseconds win over seconds",
			headers: map[string]string{RetryAfterMSHeader: "250", RetryAfterHeader: "12"},
			wantMin: 250 * time.Millisecond,
			wantMax: 250 * time.Millisecond,
		},
		{name: "an HTTP date", headers: map[string]string{RetryAfterHeader: future}, wantMin: 59 * time.Minute, wantMax: time.Hour},
		{name: "a date in the past", headers: map[string]string{RetryAfterHeader: past}},
		{name: "zero", headers: map[string]string{RetryAfterHeader: "0"}},
		{name: "garbage", headers: map[string]string{RetryAfterHeader: "soon"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{Header: http.Header{}}
			for k, v := range tt.headers {
				resp.Header.Set(k, v)
			}

			got := RetryAfter(resp)
			require.GreaterOrEqual(t, got, tt.wantMin)
			require.LessOrEqual(t, got, tt.wantMax)
		})
	}
}

func TestRetryAfterNilResponse(t *testing.T) {
	t.Parallel()

	require.Zero(t, RetryAfter(nil))
}
