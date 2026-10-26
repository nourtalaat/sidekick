package redact

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// testKey is the hash key the tests use.
var testKey = []byte("test-hash-key")

// Fake secrets in the right shapes. They are built from parts at run time so
// that secret scanners do not flag this file.
var (
	fakeAWSKeyID     = "AK" + "IA" + "TESTFAKE00000000"
	fakeAWSSecret    = strings.Repeat("Fake0", 8)
	fakeGoogleKey    = "AI" + "za" + strings.Repeat("F", 35)
	fakeKeyID        = strings.Repeat("0f", 20)
	fakeGitHubToken  = "gh" + "p_" + strings.Repeat("F4ke", 9)
	fakeGitHubPAT    = "github" + "_pat_" + strings.Repeat("F4ke", 6)
	fakeGitLabToken  = "gl" + "pat-" + strings.Repeat("F4ke", 5)
	fakeSlackToken   = "xo" + "xb-" + "0000000000-0000000000000-" + strings.Repeat("Fake", 6)
	fakeSlackApp     = "xa" + "pp-1-" + "A0000000000-0000000000000-" + strings.Repeat("0f", 8)
	fakeSlackWebhook = "https://hooks." + "slack.com/services/" + "T00000000/B00000000/" + strings.Repeat("Fake", 6)
	fakeJWT          = fakeJWTPart(`{"alg":"none","typ":"JWT"}`) + "." + fakeJWTPart(`{"sub":"fake"}`) + "." + "c2lnbmF0dXJl"
	fakePEMBody      = "\n" + strings.Repeat("RkFLRUtFWUZBS0VLRVk=", 3) + "\n"
	fakePrivateKey   = "-----BEGIN " + "RSA PRIVATE KEY-----" + fakePEMBody + "-----END " + "RSA PRIVATE KEY-----"
)

func TestTextBuiltInRules(t *testing.T) {
	t.Parallel()

	allRules := BuiltInRuleNames()

	tests := []struct {
		name    string
		builtIn []string
		in      string
		// redacted maps each value that must be replaced to its rule.
		redacted [][2]string
	}{
		// cloud_keys
		{name: "aws access key id", in: "using " + fakeAWSKeyID + " now", redacted: [][2]string{{fakeAWSKeyID, CloudKeys}}},
		{name: "aws access key id too short", in: "using " + fakeAWSKeyID[:19] + " now"},
		{name: "aws secret access key", in: "aws_secret_access_key = " + fakeAWSSecret, redacted: [][2]string{{fakeAWSSecret, CloudKeys}}},
		{name: "google api key", in: "key " + fakeGoogleKey, redacted: [][2]string{{fakeGoogleKey, CloudKeys}}},
		{name: "gcp service account key id", in: `{"private_key_id": "` + fakeKeyID + `"}`, redacted: [][2]string{{fakeKeyID, CloudKeys}}},
		{name: "gcp key id too short", in: `{"private_key_id": "` + fakeKeyID[:39] + `"}`},
		// vcs_tokens
		{name: "github token", in: "token " + fakeGitHubToken, redacted: [][2]string{{fakeGitHubToken, VCSTokens}}},
		{name: "github fine-grained token", in: "pat " + fakeGitHubPAT + ".", redacted: [][2]string{{fakeGitHubPAT, VCSTokens}}},
		{name: "github token too short", in: "token " + fakeGitHubToken[:20]},
		{name: "gitlab token", in: "PRIVATE-TOKEN: " + fakeGitLabToken, redacted: [][2]string{{fakeGitLabToken, VCSTokens}}},
		{name: "gitlab prefix in prose", in: "see glpat-docs for details"},
		{name: "token as a pair value", in: "token=" + fakeGitHubToken, redacted: [][2]string{{fakeGitHubToken, VCSTokens}}},
		// slack_tokens
		{name: "slack bot token", in: "SLACK_BOT=" + fakeSlackToken, redacted: [][2]string{{fakeSlackToken, SlackTokens}}},
		{name: "slack app token", in: "app " + fakeSlackApp, redacted: [][2]string{{fakeSlackApp, SlackTokens}}},
		{name: "slack webhook url", in: "posting to " + fakeSlackWebhook, redacted: [][2]string{{fakeSlackWebhook, SlackTokens}}},
		{name: "slack prefix too short", in: "xoxb-123"},
		// jwt
		{name: "jwt", in: "id_jwt " + fakeJWT + " end", redacted: [][2]string{{fakeJWT, JWT}}},
		{name: "jwt with one part", in: "value eyJhbGciOiJub25lIn0 alone"},
		// private_keys
		{name: "private key block", in: "key:\n" + fakePrivateKey + "\nnext line", redacted: [][2]string{{fakePrivateKey, PrivateKeys}}},
		{
			name:     "private key block with escaped newlines",
			in:       `{"k":"` + strings.ReplaceAll(fakePrivateKey, "\n", `\n`) + `"}`,
			redacted: [][2]string{{strings.ReplaceAll(fakePrivateKey, "\n", `\n`), PrivateKeys}},
		},
		{
			name:     "private key block cut short",
			in:       "-----BEGIN " + "PRIVATE KEY-----" + fakePEMBody + "(cut)",
			redacted: [][2]string{{"-----BEGIN " + "PRIVATE KEY-----" + fakePEMBody, PrivateKeys}},
		},
		{name: "public key block", in: "-----BEGIN PUBLIC KEY-----" + fakePEMBody + "-----END PUBLIC KEY-----"},
		// bearer_tokens
		{name: "authorization bearer header", in: "Authorization: Bearer abcdef", redacted: [][2]string{{"abcdef", BearerTokens}}},
		{name: "authorization basic header", in: `-H "Authorization: Basic dXNlcjpwYXNz"`, redacted: [][2]string{{"dXNlcjpwYXNz", BearerTokens}}},
		{name: "bearer token elsewhere", in: "sent bearer abc123def", redacted: [][2]string{{"abc123def", BearerTokens}}},
		{name: "bearer in prose", in: "the bearer of bad news"},
		// password_pairs
		{name: "password equals", in: "login password=hunter2 ok", redacted: [][2]string{{"hunter2", PasswordPairs}}},
		{name: "quoted secret with spaces", in: `"db_password": "two words"`, redacted: [][2]string{{"two words", PasswordPairs}}},
		{name: "yaml secret", in: "client_secret: s3cr3t\nname: app", redacted: [][2]string{{"s3cr3t", PasswordPairs}}},
		{name: "password in url", in: "dsn postgres://app" + ":pa55word@db:5432/app", redacted: [][2]string{{"pa55word", PasswordPairs}}},
		{name: "token counts", in: "max_tokens: 512, input_tokens=20"},
		{name: "secret name", in: "secretName: db-creds"},
		{name: "empty password", in: `password: ""`},
		{name: "secret on the next line", in: "secret:\n  name: db"},
		// emails
		{name: "email", in: "mail alice@example.com now", redacted: [][2]string{{"alice@example.com", Emails}}},
		{name: "two emails", in: "a.b+c@mail.example.co.uk, bob@example.org", redacted: [][2]string{{"a.b+c@mail.example.co.uk", Emails}, {"bob@example.org", Emails}}},
		{name: "email without a domain", in: "root@localhost"},
		// card_numbers
		{name: "card number", in: "card 4111111111111111 used", redacted: [][2]string{{"4111111111111111", CardNumbers}}},
		{name: "card number with spaces", in: "card 4111 1111 1111 1111.", redacted: [][2]string{{"4111 1111 1111 1111", CardNumbers}}},
		{name: "card number with hyphens", in: "card 5500-0000-0000-0004", redacted: [][2]string{{"5500-0000-0000-0004", CardNumbers}}},
		{name: "15 digit card number", in: "amex 378282246310005", redacted: [][2]string{{"378282246310005", CardNumbers}}},
		{name: "16 digits that fail the luhn check", in: "order 4111111111111112"},
		{name: "unix time in nanoseconds", in: "ts 1761475200000000000"},
		{name: "20 digits", in: "id 41111111111111110000"},
		// ip_addresses
		{name: "ipv4 off by default", in: "from 10.0.0.1"},
		{name: "ipv4", builtIn: allRules, in: "from 10.0.0.1:8080", redacted: [][2]string{{"10.0.0.1", IPAddresses}}},
		{name: "ipv6", builtIn: allRules, in: "from 2001:db8::1 ok", redacted: [][2]string{{"2001:db8::1", IPAddresses}}},
		{name: "ipv6 loopback", builtIn: allRules, in: "[::1]:80", redacted: [][2]string{{"::1", IPAddresses}}},
		{name: "ipv4 out of range", builtIn: allRules, in: "version 999.1.1.1"},
		{name: "clock time", builtIn: allRules, in: "at 12:34:56"},
		{name: "mac address", builtIn: allRules, in: "hw aa:bb:cc:dd:ee:ff"},
		{name: "scope operator", builtIn: allRules, in: "std::vector"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := newTestRedactor(t, Options{BuiltIn: tt.builtIn})
			got := r.Text(tt.in)

			want := tt.in
			wantCounts := map[string]int{}

			for _, rv := range tt.redacted {
				want = strings.Replace(want, rv[0], placeholderFor(testKey, rv[1], rv[0]), 1)
				wantCounts[rv[1]]++
			}

			if len(wantCounts) == 0 {
				wantCounts = nil
			}

			require.Equal(t, want, got.Text)
			require.Equal(t, wantCounts, got.Counts)
			require.False(t, got.Truncated)
		})
	}
}

func TestTextPlaceholders(t *testing.T) {
	t.Parallel()

	const email = "alice@example.com"

	tests := []struct {
		name      string
		keyA      []byte
		keyB      []byte
		valueA    string
		valueB    string
		wantEqual bool
	}{
		{name: "same value and key", keyA: testKey, keyB: testKey, valueA: email, valueB: email, wantEqual: true},
		{name: "different key", keyA: testKey, keyB: []byte("another-key"), valueA: email, valueB: email},
		{name: "different value", keyA: testKey, keyB: testKey, valueA: email, valueB: "bob@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := newTestRedactor(t, Options{HashKey: tt.keyA}).Text(tt.valueA).Text
			b := newTestRedactor(t, Options{HashKey: tt.keyB}).Text(tt.valueB).Text

			require.Regexp(t, `^\[REDACTED:emails:[0-9a-f]{6}\]$`, a)
			require.Regexp(t, `^\[REDACTED:emails:[0-9a-f]{6}\]$`, b)

			if tt.wantEqual {
				require.Equal(t, a, b)
			} else {
				require.NotEqual(t, a, b)
			}
		})
	}
}

func TestTextSameValueTwice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "one line", in: "from alice@example.com to alice@example.com"},
		{name: "two lines", in: "from alice@example.com\nreply to alice@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := newTestRedactor(t, Options{}).Text(tt.in)

			ph := placeholderFor(testKey, Emails, "alice@example.com")
			require.Equal(t, 2, strings.Count(got.Text, ph))
			require.Equal(t, map[string]int{Emails: 2}, got.Counts)
		})
	}
}

func TestNewWithoutHashKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "email", in: "alice@example.com"},
		{name: "password", in: "password=hunter2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			logger := slog.New(slog.NewTextHandler(&logs, nil))

			a, err := New(Options{Logger: logger})
			require.NoError(t, err)

			b, err := New(Options{Logger: logger})
			require.NoError(t, err)

			require.Contains(t, logs.String(), "level=WARN")
			require.Contains(t, logs.String(), "change after a restart")
			require.Equal(t, a.Text(tt.in).Text, b.Text(tt.in).Text, "redactors in one process share the random key")
			require.NotEqual(t, newTestRedactor(t, Options{}).Text(tt.in).Text, a.Text(tt.in).Text)
		})
	}
}

func TestNewErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    Options
		wantErr error
		wantMsg string
	}{
		{name: "unknown built-in rule", opts: Options{BuiltIn: []string{"emals"}}, wantErr: ErrUnknownRule, wantMsg: `built-in rules: rule "emals": unknown built-in rule`},
		{name: "bad custom name", opts: Options{Custom: []Rule{{Name: "Customer ID", Pattern: "x"}}}, wantErr: ErrBadRule, wantMsg: `custom rules: rule 0: invalid custom rule: rule name "Customer ID" must start with`},
		{name: "built-in name", opts: Options{Custom: []Rule{{Name: Emails, Pattern: "x"}}}, wantErr: ErrBadRule, wantMsg: `rule name "emails" is the name of a built-in rule`},
		{name: "duplicate custom name", opts: Options{Custom: []Rule{{Name: "a", Pattern: "x"}, {Name: "a", Pattern: "y"}}}, wantErr: ErrBadRule, wantMsg: `custom rules: rule "a": invalid custom rule: defined more than once`},
		{name: "empty pattern", opts: Options{Custom: []Rule{{Name: "a"}}}, wantErr: ErrBadRule, wantMsg: `custom rules: rule "a": invalid custom rule: empty pattern`},
		{name: "bad pattern", opts: Options{Custom: []Rule{{Name: "a", Pattern: "("}}}, wantErr: ErrBadRule, wantMsg: "missing closing )"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := New(tt.opts)
			require.Nil(t, r)
			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorContains(t, err, tt.wantMsg)
		})
	}
}

func TestTextRuleSelection(t *testing.T) {
	t.Parallel()

	const in = "mail alice@example.com about cus_0123456789abcd from 10.0.0.1"

	tests := []struct {
		name string
		opts Options
		want map[string]int
	}{
		{name: "default rules", opts: Options{}, want: map[string]int{Emails: 1}},
		{name: "no built-in rules", opts: Options{BuiltIn: []string{}}},
		{name: "chosen built-in rules", opts: Options{BuiltIn: []string{IPAddresses}}, want: map[string]int{IPAddresses: 1}},
		{
			name: "custom rule with a group",
			opts: Options{BuiltIn: []string{}, Custom: []Rule{{Name: "customer_id", Pattern: "(cus|sub)_[A-Za-z0-9]{14}"}}},
			want: map[string]int{"customer_id": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := newTestRedactor(t, tt.opts).Text(in)
			require.Equal(t, tt.want, got.Counts)

			if tt.want["customer_id"] == 1 {
				require.Contains(t, got.Text, " about "+placeholderFor(testKey, "customer_id", "cus_0123456789abcd")+" from ")
			}
		})
	}
}

func TestTextTwice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "password pair", in: "password=hunter2"},
		{name: "mixed", in: "Authorization: Bearer abc123 from alice@example.com with " + fakeGitHubToken},
		{name: "placeholder as a value", in: "password=[REDACTED:password_pairs:abcdef]&next=1"},
		{name: "quoted placeholder as a value", in: `password="[REDACTED:password_pairs:abcdef]" url=postgres://app:[REDACTED:password_pairs:abcdef]@db`},
		{name: "placeholders after a bearer", in: "Authorization: Bearer [REDACTED:bearer_tokens:abcdef] then bearer [REDACTED:jwt:abcdef]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := newTestRedactor(t, Options{})
			once := r.Text(tt.in)
			twice := r.Text(once.Text)

			require.Equal(t, once.Text, twice.Text)
			require.Nil(t, twice.Counts)
		})
	}
}

func TestTextFakePlaceholder(t *testing.T) {
	t.Parallel()

	const fake = "[REDACTED:x:000000]"

	tests := []struct {
		name   string
		opts   Options
		in     string
		secret string
	}{
		{name: "quoted password", in: `password="` + fake + `hunter2"`, secret: "hunter2"},
		{name: "unquoted password", in: "password=" + fake + "hunter2", secret: "hunter2"},
		{name: "bearer token", in: "Authorization: Bearer " + fake + "tok123", secret: "tok123"},
		{name: "bearer token without a digit", in: "sent bearer " + fake + "abc", secret: "abc"},
		{name: "aws secret access key", in: "aws_secret_access_key=" + fake + fakeAWSSecret, secret: fakeAWSSecret[:10]},
		{name: "url password", in: "postgres://app" + ":" + fake + "pa55word@db/app", secret: "pa55word"},
		{
			name:   "custom rule",
			opts:   Options{Custom: []Rule{{Name: "customer_id", Pattern: "cus_[A-Za-z0-9]{14}"}}},
			in:     fake + "cus_0123456789abcd",
			secret: "cus_0123456789abcd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := newTestRedactor(t, tt.opts)
			once := r.Text(tt.in)

			require.NotContains(t, once.Text, tt.secret)
			require.NotEmpty(t, once.Counts)

			twice := r.Text(once.Text)
			require.Equal(t, once.Text, twice.Text, "redacting the result again changes nothing")
			require.Nil(t, twice.Counts)
		})
	}
}

func TestTextTruncation(t *testing.T) {
	t.Parallel()

	secretAtCut := strings.Repeat("a", MaxSize-10) + " " + fakeGitHubToken + strings.Repeat(" b", 100)

	tests := []struct {
		name          string
		in            string
		wantTruncated bool
	}{
		{name: "at the limit", in: strings.Repeat("a", MaxSize)},
		{name: "one byte over", in: strings.Repeat("a", MaxSize+1), wantTruncated: true},
		{name: "far over", in: strings.Repeat("a", 4*MaxSize), wantTruncated: true},
		{name: "multi-byte runes", in: strings.Repeat("é", MaxSize), wantTruncated: true},
		{name: "placeholders grow the text", in: strings.Repeat("ab@example.com ", MaxSize/15), wantTruncated: true},
		{name: "secret across the cut", in: secretAtCut, wantTruncated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := newTestRedactor(t, Options{}).Text(tt.in)

			require.Equal(t, tt.wantTruncated, got.Truncated)
			require.LessOrEqual(t, len(got.Text), MaxSize)
			require.True(t, utf8.ValidString(got.Text))
			require.NotContains(t, got.Text, fakeGitHubToken[:8])

			last := strings.LastIndex(got.Text, "[REDACTED:")
			if last >= 0 {
				require.Regexp(t, `^\[REDACTED:[a-z_]+:[0-9a-f]{6}\]`, got.Text[last:], "the cut does not split a placeholder")
			}
		})
	}
}

func TestTextConcurrent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "mixed", in: "password=hunter2 alice@example.com " + fakeJWT + " 4111111111111111"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const goroutines = 16

			r := newTestRedactor(t, Options{})
			want := r.Text(tt.in).Text

			var wg sync.WaitGroup

			got := make([]string, goroutines)
			for i := range goroutines {
				wg.Go(func() { got[i] = r.Text(tt.in).Text })
			}

			wg.Wait()

			for _, g := range got {
				require.Equal(t, want, g)
			}
		})
	}
}

// BenchmarkTextHostile redacts a long line built from near misses for every
// rule, which keeps the regular expressions busy without many matches. The
// time per byte stays flat from 64 KiB to 256 KiB.
func BenchmarkTextHostile(b *testing.B) {
	r, err := New(Options{BuiltIn: BuiltInRuleNames(), HashKey: testKey})
	require.NoError(b, err)

	for _, size := range []int{64 << 10, 128 << 10, 256 << 10} {
		line := hostileLine(size)

		b.Run(sizeName(size), func(b *testing.B) {
			b.SetBytes(int64(len(line)))

			for b.Loop() {
				r.Text(line)
			}
		})
	}
}

// hostileLine returns a line of exactly size bytes made of partial matches:
// key headers without a body or end, unclosed quotes, token prefixes that are
// too short, digit runs that fail the Luhn check, and long runs of email and
// IP address characters.
func hostileLine(size int) string {
	unit := strings.Join([]string{
		"-----BEGIN " + "RSA PRIVATE KEY-----",
		"AK" + "IA" + "0000",
		"eyJ" + "aaaa.eyJ" + "bbbb",
		"gh" + "p_" + "short",
		"xo" + "xb-1",
		`password="unclosed`,
		"Authorization: Bearer",
		"bearer of",
		strings.Repeat("a.", 20) + "@" + strings.Repeat("b.", 20),
		"4111 1111 1111 1112",
		"1.2.3.",
		"fe80:" + strings.Repeat(":abc", 8),
		"postgres://user:",
	}, " ")

	line := strings.Repeat(unit+" ", size/(len(unit)+1)+1)

	return line[:size]
}

func sizeName(size int) string {
	return fmt.Sprintf("%dKiB", size>>10)
}

func newTestRedactor(t *testing.T, opts Options) *Redactor {
	t.Helper()

	if opts.HashKey == nil {
		opts.HashKey = testKey
	}

	r, err := New(opts)
	require.NoError(t, err)

	return r
}

// placeholderFor computes a placeholder the way the package documents it.
func placeholderFor(key []byte, rule, value string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))

	return "[REDACTED:" + rule + ":" + hex.EncodeToString(mac.Sum(nil))[:6] + "]"
}

func fakeJWTPart(json string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(json))
}
