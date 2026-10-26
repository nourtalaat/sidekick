package intake

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBearer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		token   string
		header  string
		wantMsg string
	}{
		{name: "right token", token: "s3cret", header: "Bearer s3cret"},
		{name: "scheme in other case", token: "s3cret", header: "bearer s3cret"},
		{name: "wrong token", token: "s3cret", header: "Bearer s3cre", wantMsg: "wrong bearer token"},
		{name: "longer token", token: "s3cret", header: "Bearer s3cret2", wantMsg: "wrong bearer token"},
		{name: "no header", token: "s3cret", wantMsg: "missing bearer token"},
		{name: "other scheme", token: "s3cret", header: "Basic s3cret", wantMsg: "missing bearer token"},
		{name: "scheme only", token: "s3cret", header: "Bearer", wantMsg: "missing bearer token"},
		{name: "empty token accepts nothing", header: "Bearer ", wantMsg: "no token is configured"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := Request{Header: http.Header{}}
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			err := Bearer{Token: []byte(tt.token)}.Authenticate(req)
			if tt.wantMsg == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrUnauthenticated)
			require.ErrorContains(t, err, tt.wantMsg)
		})
	}
}

func TestHMAC(t *testing.T) {
	t.Parallel()

	secret := []byte("hmac-secret")
	body := []byte(`{"alert":"disk full"}`)
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	good := SignatureHeader(secret, ts, body)

	tests := []struct {
		name      string
		secret    []byte
		timestamp string
		signature string
		body      []byte
		wantMsg   string
	}{
		{name: "valid signature", timestamp: ts, signature: good},
		{name: "timestamp at the edge of the tolerance", timestamp: strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10), signature: SignatureHeader(secret, strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10), body)},
		{name: "timestamp too old", timestamp: strconv.FormatInt(now.Add(-5*time.Minute-time.Second).Unix(), 10), signature: SignatureHeader(secret, strconv.FormatInt(now.Add(-5*time.Minute-time.Second).Unix(), 10), body), wantMsg: "timestamp outside tolerance"},
		{name: "timestamp in the future", timestamp: strconv.FormatInt(now.Add(6*time.Minute).Unix(), 10), signature: SignatureHeader(secret, strconv.FormatInt(now.Add(6*time.Minute).Unix(), 10), body), wantMsg: "timestamp outside tolerance"},
		{name: "missing timestamp", signature: good, wantMsg: "parse timestamp"},
		{name: "timestamp not a number", timestamp: "yesterday", signature: good, wantMsg: "parse timestamp"},
		{name: "missing signature", timestamp: ts, wantMsg: "missing signature"},
		{name: "signature without prefix", timestamp: ts, signature: good[len("sha256="):], wantMsg: "missing signature"},
		{name: "signature not hex", timestamp: ts, signature: "sha256=zz", wantMsg: "decode signature"},
		{name: "wrong secret", timestamp: ts, signature: SignatureHeader([]byte("other"), ts, body), wantMsg: "wrong signature"},
		{name: "changed body", timestamp: ts, signature: good, body: []byte(`{"alert":"disk ok"}`), wantMsg: "wrong signature"},
		{name: "signature for another timestamp", timestamp: strconv.FormatInt(now.Unix()-1, 10), signature: good, wantMsg: "wrong signature"},
		{name: "empty secret accepts nothing", secret: []byte{}, timestamp: ts, signature: SignatureHeader(nil, ts, body), wantMsg: "no HMAC secret is configured"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := Request{Header: http.Header{}, Body: body, ReceivedAt: now}
			if tt.body != nil {
				req.Body = tt.body
			}

			if tt.timestamp != "" {
				req.Header.Set(HeaderTimestamp, tt.timestamp)
			}

			if tt.signature != "" {
				req.Header.Set(HeaderSignature, tt.signature)
			}

			key := secret
			if tt.secret != nil {
				key = tt.secret
			}

			err := HMAC{Secret: key, Tolerance: 5 * time.Minute}.Authenticate(req)
			if tt.wantMsg == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrUnauthenticated)
			require.ErrorContains(t, err, tt.wantMsg)
		})
	}
}

func TestSign(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		secret    string
		timestamp string
		body      string
		want      string
	}{
		{
			// Computed with: printf '%s' '1800000000.{}' | openssl dgst -sha256 -hmac key
			name:      "known value",
			secret:    "key",
			timestamp: "1800000000",
			body:      "{}",
			want:      "sha256=7321475872b03ff21bdc2dfd86ea2d99ec29c479fbe4a77aa6acf4509505493f",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, SignatureHeader([]byte(tt.secret), tt.timestamp, []byte(tt.body)))
		})
	}
}

func TestParseSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "critical", want: "critical"},
		{in: " CRITICAL ", want: "critical"},
		{in: "page", want: "critical"},
		{in: "error", want: "critical"},
		{in: "Warning", want: "warning"},
		{in: "warn", want: "warning"},
		{in: "info", want: "info"},
		{in: "none", want: "info"},
		{in: "", want: "unknown"},
		{in: "sev-9", want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, string(ParseSeverity(tt.in)))
		})
	}
}
