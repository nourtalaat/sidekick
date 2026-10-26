package redact

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
)

// The names of the built-in rules, as config and placeholders use them.
const (
	CloudKeys     = "cloud_keys"
	VCSTokens     = "vcs_tokens"
	SlackTokens   = "slack_tokens"
	JWT           = "jwt"
	PrivateKeys   = "private_keys"
	BearerTokens  = "bearer_tokens"
	PasswordPairs = "password_pairs"
	Emails        = "emails"
	CardNumbers   = "card_numbers"
	IPAddresses   = "ip_addresses"
)

// MaxRuleNameLen is the longest rule name, so a placeholder stays short.
const MaxRuleNameLen = 32

// ErrUnknownRule is returned by New for a built-in rule name it does not know.
var ErrUnknownRule = errors.New("unknown built-in rule")

// ErrBadRule is returned by New for a custom rule it cannot use.
var ErrBadRule = errors.New("invalid custom rule")

// ruleNamePattern is what a rule name must look like, so placeholders can be
// found again in text that was already redacted.
var ruleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// secretKeyWords are the key names that password_pairs treats as holding a
// secret. A key matches when it ends in one of them, so "db_password" and
// "client_secret" match, but "max_tokens" and "secretName" do not.
const secretKeyWords = `(?:password|passwd|passphrase|pwd|secret|secret[_-]?key|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|credentials?)`

// secretKeyHints are lower-case strings, one of which every key that ends in
// secretKeyWords holds.
var secretKeyHints = []string{"pass", "pwd", "secret", "token", "key", "credential"}

// builtIns are the built-in rules in priority order: when two matches start
// at the same place, the rule that comes first here wins. Specific shapes
// such as tokens come before the general ones such as key-value pairs.
var builtIns = []rule{
	{
		name: CloudKeys,
		patterns: []pattern{
			// AWS access key IDs.
			{re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA|A3T[A-Z0-9])[A-Z0-9]{16}\b`), hints: []string{"akia", "asia", "abia", "acca", "a3t"}},
			// AWS secret access keys, which only their key name identifies.
			{re: regexp.MustCompile(`(?i)aws_?secret_?access_?key["']?[ \t]*[:=][ \t]*["']?([A-Za-z0-9/+=]{40})\b`), group: true, hints: []string{"aws"}},
			// Google API keys.
			{re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), hints: []string{"aiza"}},
			// The key ID in a GCP service account key file. The private key
			// in the same file is a PEM block, which private_keys finds.
			{re: regexp.MustCompile(`"private_key_id"[ \t]*:[ \t]*"([0-9a-f]{40})"`), group: true, hints: []string{"private_key_id"}},
		},
		members: []memberPattern{
			{key: regexp.MustCompile(`^private_key_id$`), value: regexp.MustCompile(`^[0-9a-f]{40}$`)},
		},
	},
	{
		name: VCSTokens,
		patterns: []pattern{
			// GitHub personal, OAuth, user-to-server, server-to-server and
			// refresh tokens, and fine-grained personal access tokens.
			{re: regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b`), hints: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}},
			{re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}\b`), hints: []string{"github_pat_"}},
			// GitLab tokens and runner registration tokens.
			{re: regexp.MustCompile(`\bgl(?:pat|dt|rt|ptt|soat|cbt|imt|agent|oas|ffct)-[A-Za-z0-9_-][A-Za-z0-9_.-]{18,}[A-Za-z0-9_-]`), hints: []string{"glpat-", "gldt-", "glrt-", "glptt-", "glsoat-", "glcbt-", "glimt-", "glagent-", "gloas-", "glffct-"}},
			{re: regexp.MustCompile(`\bGR1348941[A-Za-z0-9_-]{20,}`), hints: []string{"gr1348941"}},
		},
	},
	{
		name: SlackTokens,
		patterns: []pattern{
			{re: regexp.MustCompile(`\bxox(?:[abposr]|e(?:\.xox[pb])?)-[A-Za-z0-9-]{10,}`), hints: []string{"xox"}},
			{re: regexp.MustCompile(`\bxapp-[0-9]-[A-Za-z0-9-]{10,}`), hints: []string{"xapp-"}},
			{re: regexp.MustCompile(`https://hooks\.slack\.com/(?:services|workflows|triggers)/[A-Za-z0-9/_+-]+`), hints: []string{"hooks.slack.com"}},
		},
	},
	{
		name: JWT,
		patterns: []pattern{
			{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{2,}\.eyJ[A-Za-z0-9_-]{2,}\.[A-Za-z0-9_-]*`), hints: []string{"eyj"}},
		},
	},
	{
		name: PrivateKeys,
		patterns: []pattern{
			// A whole block.
			{re: regexp.MustCompile(`-----BEGIN[A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----(?s:.*?)-----END[A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`), hints: []string{"private key"}},
			// A block whose end was cut off: the header and the base64 that
			// follows it. When the whole block is there, the longer match
			// above wins.
			{re: regexp.MustCompile(`-----BEGIN[A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----[A-Za-z0-9+/=\s\\]*`), hints: []string{"private key"}},
		},
	},
	{
		name: BearerTokens,
		patterns: []pattern{
			// Any credential in an Authorization header.
			{re: regexp.MustCompile(`(?i)\b(?:proxy-)?authorization["']?[ \t]*[:=][ \t]*["']?(?:bearer|basic|token)[ \t]+([A-Za-z0-9._~+/-]+=*)`), group: true, hints: []string{"authorization"}},
			// "Bearer <token>" anywhere else, when the token holds a digit or
			// is long, so prose such as "the bearer of" is kept.
			{re: regexp.MustCompile(`(?i)\bbearer[ \t]+([A-Za-z0-9._~+/-]*[0-9][A-Za-z0-9._~+/-]*=*|[A-Za-z0-9._~+/-]{16,}=*)`), group: true, hints: []string{"bearer"}},
		},
		members: []memberPattern{
			{key: regexp.MustCompile(`(?i)^(?:proxy-)?authorization$`), value: regexp.MustCompile(`(?i)^(?:bearer|basic|token)[ \t]+(\S+)$`)},
		},
	},
	{
		name: PasswordPairs,
		patterns: []pattern{
			// key=value and key: value, with the value quoted or not.
			{re: regexp.MustCompile(`(?i)` + secretKeyWords + `["']?[ \t]*[:=][ \t]*(?:"([^"\r\n]+)"|'([^'\r\n]+)'|([^\s"'` + "`" + `,;&)}\]]+))`), group: true, hints: secretKeyHints},
			// The password in a URL such as postgres://user:password@host.
			{re: regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^\s:/?#@]+:([^\s/?#@]+)@`), group: true, hints: []string{"://"}},
		},
		members: []memberPattern{
			{key: regexp.MustCompile(`(?i)^[\w.-]*` + secretKeyWords + `$`)},
		},
	},
	{
		name: Emails,
		patterns: []pattern{
			{re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}\b`), hints: []string{"@"}},
		},
	},
	{
		name: CardNumbers,
		patterns: []pattern{
			// 13 to 19 digits, optionally in groups split by spaces or
			// hyphens, starting with a digit card networks use, and passing
			// the Luhn check.
			{re: regexp.MustCompile(`\b[2-6](?:[ -]?[0-9]){12,18}\b`), valid: luhn},
		},
	},
	{
		name:       IPAddresses,
		offDefault: true,
		patterns: []pattern{
			{re: regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\b`)},
			// IPv6 candidates, checked by parsing them. The character before
			// one is matched outside the group, because RE2 has no
			// look-behind.
			{re: regexp.MustCompile(`(?:^|[^0-9A-Za-z_:.])((?:[0-9A-Fa-f]{0,4}:){2,7}(?:[0-9A-Fa-f]{1,4}|(?:[0-9]{1,3}\.){3}[0-9]{1,3})?)`), group: true, valid: isIPv6},
		},
	},
}

// rule is one named rule: built-in or custom.
type rule struct {
	name string
	// patterns find the rule's matches in text.
	patterns []pattern
	// members find matches in JSON object members by their key, for string
	// values the patterns find nothing in.
	members []memberPattern
	// offDefault keeps a built-in rule out of DefaultRuleNames.
	offDefault bool
}

// pattern is one regular expression of a rule.
type pattern struct {
	re *regexp.Regexp
	// group redacts only the first capture group that took part in the
	// match, leaving the rest, such as a key name, in place.
	group bool
	// valid, when set, must accept the matched text for it to be redacted.
	valid func(string) bool
	// hints, when set, are lower-case strings of which any text the pattern
	// matches holds at least one. Text without any of them is not scanned,
	// which keeps redaction of ordinary text fast.
	hints []string
}

// memberPattern matches a JSON object member whose key says its string
// value is secret.
type memberPattern struct {
	key *regexp.Regexp
	// value, when set, must match the value. Its first capture group, if it
	// has one, is the part redacted; otherwise the whole value is.
	value *regexp.Regexp
}

// BuiltInRuleNames returns the names of every built-in rule, in priority
// order.
func BuiltInRuleNames() []string {
	names := make([]string, len(builtIns))
	for i, r := range builtIns {
		names[i] = r.name
	}

	return names
}

// DefaultRuleNames returns the built-in rules used when Options.BuiltIn is
// nil: every built-in rule except ip_addresses, because IP addresses are
// often useful evidence.
func DefaultRuleNames() []string {
	var names []string

	for _, r := range builtIns {
		if !r.offDefault {
			names = append(names, r.name)
		}
	}

	return names
}

// CheckRuleName reports whether name can name a custom rule: it must look
// like the built-in names and must not be one of them.
func CheckRuleName(name string) error {
	if !ruleNamePattern.MatchString(name) {
		return fmt.Errorf("rule name %q must start with a lower-case letter and hold only lower-case letters, digits, \"_\" and \"-\", at most %d characters", name, MaxRuleNameLen)
	}

	if slices.Contains(BuiltInRuleNames(), name) {
		return fmt.Errorf("rule name %q is the name of a built-in rule", name)
	}

	return nil
}

// match returns the span of value that m redacts in an object member
// with the given key.
func (m memberPattern) match(key, value string) (start, end int, ok bool) {
	if value == "" || !m.key.MatchString(key) {
		return 0, 0, false
	}

	if m.value == nil {
		return 0, len(value), true
	}

	loc := m.value.FindStringSubmatchIndex(value)
	if loc == nil {
		return 0, 0, false
	}

	if start, end := firstGroup(loc); start >= 0 {
		return start, end, true
	}

	return loc[0], loc[1], true
}

// luhn reports whether the digits in s pass the Luhn check and there are
// as many as a card number has. Other characters are skipped.
func luhn(s string) bool {
	const (
		minDigits = 13
		maxDigits = 19
	)

	sum, n := 0, 0

	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}

		d := int(c - '0')
		if n%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}

		sum += d
		n++
	}

	return n >= minDigits && n <= maxDigits && sum%10 == 0
}

// isIPv6 reports whether s is an IPv6 address.
func isIPv6(s string) bool {
	addr, err := netip.ParseAddr(s)

	return err == nil && addr.Is6()
}
