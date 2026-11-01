package scenario

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source/mock"
)

func TestLoosen(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantArgs    map[string]mock.Matcher
		wantDropped []string
		wantErr     bool
	}{
		{
			name:        "identifying arguments are kept, the rest dropped",
			input:       `{"service":"checkout-api","from":"2026-05-04T09:00:00Z","limit":5}`,
			wantArgs:    map[string]mock.Matcher{"service": {Equals: "checkout-api"}},
			wantDropped: []string{"from", "limit"},
		},
		{
			name:     "names are compared without case, underscores and hyphens",
			input:    `{"Pod_Name":"checkout-0","monitor-id":42}`,
			wantArgs: map[string]mock.Matcher{"Pod_Name": {Equals: "checkout-0"}, "monitor-id": {Equals: int64(42)}},
		},
		{
			name:     "a number with a fraction",
			input:    `{"id":1.5}`,
			wantArgs: map[string]mock.Matcher{"id": {Equals: 1.5}},
		},
		{
			name:     "a commit ID becomes a prefix regex",
			input:    `{"repo":"acme/checkout","sha":"ABCDEF1234567"}`,
			wantArgs: map[string]mock.Matcher{"repo": {Equals: "acme/checkout"}, "sha": {Regex: "^ABCDEF1"}},
		},
		{
			name:     "a ref that is a branch name stays equals",
			input:    `{"ref":"main"}`,
			wantArgs: map[string]mock.Matcher{"ref": {Equals: "main"}},
		},
		{
			name:        "a free-text query is used when nothing identifies the call",
			input:       `{"query":"error rate checkout","limit":10}`,
			wantArgs:    map[string]mock.Matcher{"query": {Regex: queryMarker + "error rate checkout"}},
			wantDropped: []string{"limit"},
		},
		{
			name:        "a free-text query is dropped when an argument identifies the call",
			input:       `{"query":"error rate checkout","service":"checkout-api"}`,
			wantArgs:    map[string]mock.Matcher{"service": {Equals: "checkout-api"}},
			wantDropped: []string{"query"},
		},
		{
			name:        "a query without words gives no matcher",
			input:       `{"q":"a b"}`,
			wantArgs:    map[string]mock.Matcher{},
			wantDropped: []string{"q"},
		},
		{
			name:        "an empty value is dropped",
			input:       `{"service":"","name":"x"}`,
			wantArgs:    map[string]mock.Matcher{"name": {Equals: "x"}},
			wantDropped: []string{"service"},
		},
		{name: "no input", input: ``, wantArgs: map[string]mock.Matcher{}},
		{name: "input that is not an object", input: `[1]`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := loosen(json.RawMessage(tt.input))
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantArgs, got.args)
			require.Equal(t, tt.wantDropped, got.dropped)
		})
	}
}

func TestTermsRegex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "the three longest words",
			query: `rate(db_pool_wait_seconds_sum{instance="host-1:9100"}[5m])`,
			want:  "(?i)db_pool_wait_seconds_sum|instance|host-1",
		},
		{name: "words are lower-cased once", query: "Timeout timeout POOL", want: "(?i)timeout|pool"},
		{name: "special characters are escaped", query: "a.b(c) status.code", want: `(?i)status\.code`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, termsRegex(tt.query))
		})
	}
}
