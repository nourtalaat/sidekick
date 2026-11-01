package scrub

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

var testKey = []byte("test-hash-key")

// newCleaner builds a cleaner for a test.
func newCleaner(t *testing.T) *Cleaner {
	t.Helper()

	c, err := New(Options{HashKey: testKey})
	require.NoError(t, err)

	return c
}

// parse reads a YAML document.
func parse(t *testing.T, src string) *yaml.Node {
	t.Helper()

	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &doc))

	return &doc
}

// decode returns the document as plain values.
func decode(t *testing.T, doc *yaml.Node) map[string]any {
	t.Helper()

	var out map[string]any
	require.NoError(t, doc.Decode(&out))

	return out
}

func TestSessionClean(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want map[string]any
	}{
		{
			name: "url host, user name and percent-encoded email",
			src:  `text: 'dial https://db-prod-3.acme.com:5432/orders?user=alice&email=alice%40acme.com failed'`,
			want: map[string]any{"text": "dial https://host-1:5432/orders?user=user-2&email=user-1 failed"},
		},
		{
			name: "json inside a json string",
			src:  `text: '{"msg":"{\"host\":\"db-prod-3\",\"user\":\"alice\"}"}'`,
			want: map[string]any{"text": `{"msg":"{\"host\":\"host-1\",\"user\":\"user-1\"}"}`},
		},
		{
			name: "log line with a host, an address and a user",
			src:  `text: '10:10:04 ERROR pq: timeout dialing db-prod-3.acme.com (10.2.3.4) for user=alice; retry on db-prod-3.acme.com'`,
			want: map[string]any{"text": "10:10:04 ERROR pq: timeout dialing host-1 (ip-1) for user=user-1; retry on host-1"},
		},
		{
			name: "the same host in the event, a fixture and the cause",
			src: `
event:
  labels:
    instance: 'db-prod-3.acme.com:9100'
fixture: 'connect to db-prod-3.acme.com failed'
cause: 'Pool on DB-PROD-3.ACME.COM exhausted'
`,
			want: map[string]any{
				"event":   map[string]any{"labels": map[string]any{"instance": "host-1:9100"}},
				"fixture": "connect to host-1 failed",
				"cause":   "Pool on host-1 exhausted",
			},
		},
		{
			name: "words that only contain a user name are kept",
			src: `
author: bobby
text: 'bobby and bobbycat and web-bobby-2 ok'
`,
			want: map[string]any{"author": "user-1", "text": "user-1 and bobbycat and web-bobby-2 ok"},
		},
		{
			name: "generic names are kept",
			src: `
user: admin
text: 'admin logged in'
`,
			want: map[string]any{"user": "admin", "text": "admin logged in"},
		},
		{
			name: "an email in any case",
			src:  `text: 'mail alice@acme.com and ALICE@ACME.COM'`,
			want: map[string]any{"text": "mail user-1 and user-1"},
		},
		{
			name: "an account number and the same number in an ARN",
			src: `
account_id: 123456789012
role: 'arn:aws:iam::123456789012:role/deployer'
`,
			want: map[string]any{"account_id": "account-1", "role": "arn:aws:iam::account-1:role/deployer"},
		},
		{
			name: "ipv6 and ipv4 addresses",
			src:  `text: 'from 2001:db8::1 to 10.0.0.1'`,
			want: map[string]any{"text": "from ip-1 to ip-2"},
		},
		{
			name: "a person's name under a key stands for the first name too",
			src: `
author: 'Rhea Okafor'
text: 'commander Rhea approved'
`,
			want: map[string]any{"author": "user-1", "text": "commander user-1 approved"},
		},
		{
			name: "public hosts stay",
			src:  `text: 'see https://github.com/acme/checkout/commit/abc123'`,
			want: map[string]any{"text": "see https://github.com/acme/checkout/commit/abc123"},
		},
		{
			name: "a matcher value takes the kind of its argument",
			src: `
match:
  args:
    hostname:
      equals: db-prod-3
text: 'db-prod-3 is down'
`,
			want: map[string]any{
				"match": map[string]any{"args": map[string]any{"hostname": map[string]any{"equals": "host-1"}}},
				"text":  "host-1 is down",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doc := parse(t, tt.src)

			_, err := newCleaner(t).NewSession().Clean(doc)
			require.NoError(t, err)
			require.Equal(t, tt.want, decode(t, doc))

			// Cleaning cleaned text changes nothing.
			_, err = newCleaner(t).NewSession().Clean(doc)
			require.NoError(t, err)
			require.Equal(t, tt.want, decode(t, doc))
		})
	}
}

func TestSessionCleanRedactsSecretsInURLs(t *testing.T) {
	t.Parallel()

	doc := parse(t, `dsn: 'postgres://app_user:s3cret@db-prod-3:5432/orders'`)

	_, err := newCleaner(t).NewSession().Clean(doc)
	require.NoError(t, err)

	got := decode(t, doc)["dsn"].(string)
	require.Contains(t, got, "postgres://user-1:[REDACTED:password_pairs:")
	require.Contains(t, got, "@host-1:5432/orders")
	require.NotContains(t, got, "s3cret")
}

func TestSessionCleanText(t *testing.T) {
	t.Parallel()

	s := newCleaner(t).NewSession()

	_, err := s.Clean(parse(t, `text: 'db-prod-3.acme.com is down'`))
	require.NoError(t, err)

	got, err := s.CleanText("new host other.acme.com and db-prod-3.acme.com")
	require.NoError(t, err)
	require.Equal(t, "new host host-2 and host-1", got)
}

func TestSessionCleanCandidates(t *testing.T) {
	t.Parallel()

	const token = "tokXYZ1234567890tokXYZ123456"

	doc := parse(t, `text: 'see https://github.com/acme/checkout/commit/abc123def4567890abc123def4567890abc123de and trace `+token+` end'`)

	got, err := newCleaner(t).NewSession().Clean(doc)
	require.NoError(t, err)

	text := decode(t, doc)["text"].(string)

	require.Len(t, got, 2)
	require.Equal(t, "text", got[0].Path)
	require.Equal(t, "acme", text[got[0].Start:got[0].End])
	require.Contains(t, got[0].Reason, "owner")
	require.Equal(t, token, text[got[1].Start:got[1].End])
	require.Contains(t, got[1].Reason, "opaque ID")
}

func TestCleanerCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want []Leak
	}{
		{name: "clean", src: `msg: 'host-1 and user-2 and ip-3 and [REDACTED:emails:0a1b2c]'`},
		{name: "email", src: `msg: 'mail bob@acme.com'`, want: []Leak{{Path: "msg", Kind: "email"}, {Path: "msg", Kind: "host"}}},
		{name: "address in a nested field", src: "a:\n  b:\n    - 'from 10.1.2.3'", want: []Leak{{Path: "a.b[0]", Kind: "ip"}}},
		{name: "url host without a dot", src: `msg: 'GET http://db-prod-3/health'`, want: []Leak{{Path: "msg", Kind: "host"}}},
		{name: "public host", src: `msg: 'https://github.com/a/b and http://localhost:8080 and https://signoz.example/x'`},
		{name: "placeholder url host", src: `msg: 'http://host-1:5432/x'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := newCleaner(t).Check(parse(t, tt.src))
			if tt.want == nil {
				require.NoError(t, err)

				return
			}

			var leak *LeakError

			require.ErrorAs(t, err, &leak)
			require.Equal(t, tt.want, leak.Leaks)
			require.NotContains(t, err.Error(), "bob")
		})
	}
}
