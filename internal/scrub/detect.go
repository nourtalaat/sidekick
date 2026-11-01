package scrub

import (
	"net/url"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/nourtalaat/sidekick/internal/redact"
)

// Leak kinds.
const (
	leakEmail = "email"
	leakHost  = "host"
	leakIP    = "ip"
)

// leaks returns the kinds of sensitive values text still holds: emails,
// IP addresses, host names with a known domain suffix, and the hosts of
// URLs.
func (c *Cleaner) leaks(text string) []string {
	var kinds []string

	add := func(kind string) {
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}

	for _, sp := range c.finder.Spans(text) {
		if sp.Rule == redact.Emails {
			add(leakEmail)
		} else {
			add(leakIP)
		}
	}

	for _, m := range hostPattern.FindAllString(text, -1) {
		if hasHostSuffix(m) && !isPublicHost(strings.ToLower(m)) {
			add(leakHost)
		}
	}

	for _, raw := range urlPattern.FindAllString(text, -1) {
		if host := urlHost(raw); host != "" && !isCleanHost(host) {
			add(leakHost)
		}
	}

	return kinds
}

// urlHost returns the host of a URL found in text, in lower case, or "".
func urlHost(raw string) string {
	raw = strings.TrimRight(raw, ".,;:!?)]}'\"")

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return strings.ToLower(u.Hostname())
}

// isCleanHost reports whether a URL host needs no cleaning: a placeholder,
// a public host or a redaction placeholder.
func isCleanHost(host string) bool {
	return placeholderLike.MatchString(host) || isPublicHost(host) || strings.Contains(host, "redacted")
}

// candidatesIn returns the places in text that look like IDs, which the
// cleaner keeps because commit IDs and trace IDs are evidence.
func candidatesIn(path, text string) []Candidate {
	var out []Candidate

	add := func(start, end int, reason string) {
		out = append(out, Candidate{Path: path, Start: start, End: end, Reason: reason})
	}

	for _, loc := range uuidPattern.FindAllStringIndex(text, -1) {
		add(loc[0], loc[1], "looks like an ID (a UUID)")
	}

	for _, loc := range opaquePattern.FindAllStringIndex(text, -1) {
		if token := text[loc[0]:loc[1]]; isOpaque(token) && !uuidPattern.MatchString(token) {
			add(loc[0], loc[1], "looks like an opaque ID")
		}
	}

	for _, loc := range codeHostPath.FindAllStringSubmatchIndex(text, -1) {
		add(loc[2], loc[3], "may be the name of an organization or person (owner in a code host URL)")
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })

	return out
}

// isOpaque reports whether token mixes letters and digits and is not a hex
// digest such as a commit ID.
func isOpaque(token string) bool {
	if len(token) < minOpaqueLen || hexPattern.MatchString(token) {
		return false
	}

	var letter, digit bool

	for _, r := range token {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		}
	}

	return letter && digit
}
