package redact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSON(t *testing.T) {
	t.Parallel()

	ph := func(rule, value string) string { return placeholderFor(testKey, rule, value) }
	pem := "-----BEGIN " + "PRIVATE KEY-----" + fakePEMBody + "-----END " + "PRIVATE KEY-----"
	pemJSON := strings.ReplaceAll(pem, "\n", `\n`)

	tests := []struct {
		name       string
		in         string
		want       string
		wantCounts map[string]int
	}{
		{
			name: "nothing to redact keeps numbers and order",
			in:   `{"z": 1.50e3, "a": [true, false, null, -0, "x"], "m": {}}`,
			want: `{"z":1.50e3,"a":[true,false,null,-0,"x"],"m":{}}`,
		},
		{
			name:       "string values in nested objects and arrays",
			in:         `{"user": {"emails": ["alice@example.com", "bob@example.com"]}, "note": "from alice@example.com"}`,
			want:       `{"user":{"emails":["` + ph(Emails, "alice@example.com") + `","` + ph(Emails, "bob@example.com") + `"]},"note":"from ` + ph(Emails, "alice@example.com") + `"}`,
			wantCounts: map[string]int{Emails: 3},
		},
		{
			name:       "object keys",
			in:         `{"alice@example.com": {"logins": 3}}`,
			want:       `{"` + ph(Emails, "alice@example.com") + `":{"logins":3}}`,
			wantCounts: map[string]int{Emails: 1},
		},
		{
			name:       "value under a secret key",
			in:         `{"password": "hunter2", "db": {"client_secret": "two words"}}`,
			want:       `{"password":"` + ph(PasswordPairs, "hunter2") + `","db":{"client_secret":"` + ph(PasswordPairs, "two words") + `"}}`,
			wantCounts: map[string]int{PasswordPairs: 2},
		},
		{
			name:       "token under a secret key keeps its own rule",
			in:         `{"token": "` + fakeGitHubToken + `"}`,
			want:       `{"token":"` + ph(VCSTokens, fakeGitHubToken) + `"}`,
			wantCounts: map[string]int{VCSTokens: 1},
		},
		{
			name:       "authorization header value",
			in:         `{"headers": {"Authorization": "Bearer abc"}}`,
			want:       `{"headers":{"Authorization":"Bearer ` + ph(BearerTokens, "abc") + `"}}`,
			wantCounts: map[string]int{BearerTokens: 1},
		},
		{
			name:       "gcp service account key",
			in:         `{"type": "service_account", "private_key_id": "` + fakeKeyID + `", "private_key": "` + pemJSON + `\n"}`,
			want:       `{"type":"service_account","private_key_id":"` + ph(CloudKeys, fakeKeyID) + `","private_key":"` + ph(PrivateKeys, pem) + `\n"}`,
			wantCounts: map[string]int{CloudKeys: 1, PrivateKeys: 1},
		},
		{
			name: "keys that are not secret",
			in:   `{"secretName": "db-creds", "max_tokens": 512, "token_type": "access"}`,
			want: `{"secretName":"db-creds","max_tokens":512,"token_type":"access"}`,
		},
		{
			name: "secret key with a value that is not a string",
			in:   `{"password": null, "token": 12}`,
			want: `{"password":null,"token":12}`,
		},
		{
			name: "escapes are kept",
			in:   `{"html": "<b>&amp;</b>", "ctl": "a\u0001\t\"b\"\\", "uni": "café ☃"}`,
			want: `{"html":"<b>&amp;</b>","ctl":"a\u0001\t\"b\"\\","uni":"café ☃"}`,
		},
		{
			name:       "top-level string",
			in:         `"mail alice@example.com"`,
			want:       `"mail ` + ph(Emails, "alice@example.com") + `"`,
			wantCounts: map[string]int{Emails: 1},
		},
		{
			name:       "not JSON",
			in:         "password=hunter2\n<html>",
			want:       `"password=` + ph(PasswordPairs, "hunter2") + `\n<html>"`,
			wantCounts: map[string]int{PasswordPairs: 1},
		},
		{
			name: "two documents",
			in:   `{} {}`,
			want: `"{} {}"`,
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, stats := newTestRedactor(t, Options{}).JSON(json.RawMessage(tt.in))

			require.Equal(t, tt.want, string(got))
			require.Equal(t, tt.wantCounts, stats.Counts)
			require.False(t, stats.Truncated)

			if tt.in != "" {
				var v any
				require.NoError(t, json.Unmarshal(got, &v), "the result parses")
			}
		})
	}
}

func TestJSONSameAsText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		text string
	}{
		{name: "password", json: `{"password":"hunter2"}`, text: "password=hunter2"},
		{name: "email", json: `["alice@example.com"]`, text: "alice@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := newTestRedactor(t, Options{})
			got, _ := r.JSON(json.RawMessage(tt.json))
			text := r.Text(tt.text).Text

			_, placeholder, ok := strings.Cut(text, "[REDACTED:")
			require.True(t, ok)
			require.Contains(t, string(got), "[REDACTED:"+placeholder, "the same value gets the same placeholder in JSON and in text")
		})
	}
}

func TestJSONTwice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "secret key", in: `{"password":"hunter2","Authorization":"Bearer abc"}`},
		{name: "mixed", in: `{"a@example.com":["b@example.com","token ` + fakeGitHubToken + `"]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := newTestRedactor(t, Options{})
			once, _ := r.JSON(json.RawMessage(tt.in))
			twice, stats := r.JSON(once)

			require.Equal(t, string(once), string(twice))
			require.Nil(t, stats.Counts)
		})
	}
}

func TestJSONTruncation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		in            string
		wantTruncated bool
	}{
		{name: "under the limit", in: `["` + strings.Repeat("a", MaxSize-10) + `"]`},
		{name: "over the limit", in: `["` + strings.Repeat("a", MaxSize) + `"]`, wantTruncated: true},
		{name: "placeholders grow it over the limit", in: `[` + strings.Repeat(`"ab@example.com",`, MaxSize/17) + `""]`, wantTruncated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, stats := newTestRedactor(t, Options{}).JSON(json.RawMessage(tt.in))

			require.Equal(t, tt.wantTruncated, stats.Truncated)
			require.True(t, json.Valid(got), "the result parses")

			if tt.wantTruncated {
				var s string
				require.NoError(t, json.Unmarshal(got, &s))
				require.LessOrEqual(t, len(s), MaxSize)
			}
		})
	}
}
