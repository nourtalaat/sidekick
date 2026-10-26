// Package redact removes secrets and personal data from event text and tool
// results before they are stored or sent to a model.
//
// Each match is replaced with a placeholder [REDACTED:<rule>:<hash>], where
// the hash is the first 6 hex characters of an HMAC-SHA256 of the matched
// value. The same value always gets the same placeholder, so a model can
// still tell that two lines mention the same email without seeing it.
//
// Rules are RE2 regular expressions (Go's regexp), so the time redaction
// takes grows linearly with the size of the input. Results are cut at
// MaxSize.
package redact

import (
	"cmp"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// MaxSize is the largest result, in bytes. Longer results are cut and
// marked truncated.
const MaxSize = 256 << 10

const (
	// scanMargin is how far past MaxSize Text still looks, so a secret that
	// crosses the cut is still found and does not leak in part.
	scanMargin = 8 << 10
	// hashChars is how many hex characters of the HMAC a placeholder shows.
	hashChars = 6
	// hashKeySize is the size of the random key used when none is set.
	hashKeySize = 32
	// maxPlaceholderLen is the longest placeholder: the frame, the longest
	// rule name and the hash.
	maxPlaceholderLen = len("[REDACTED:") + MaxRuleNameLen + len(":") + hashChars + len("]")
)

// placeholderPattern finds placeholders already in a text. They are left as
// they are, so redacting a text twice changes nothing.
var placeholderPattern = regexp.MustCompile(`\[REDACTED:[a-z][a-z0-9_-]{0,31}:[0-9a-f]{6}\]`)

// processKey is the random hash key shared by every Redactor in this
// process that has no key of its own.
var processKey = sync.OnceValues(func() ([]byte, error) {
	key := make([]byte, hashKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate hash key: %w", err)
	}

	return key, nil
})

// Rule is a custom rule: a regular expression with a name. The whole match
// is redacted.
type Rule struct {
	Name    string
	Pattern string
}

// Options configure a Redactor.
type Options struct {
	// BuiltIn names the built-in rules to use. Nil means DefaultRuleNames;
	// an empty, non-nil slice turns every built-in rule off.
	BuiltIn []string
	// Custom are extra named rules.
	Custom []Rule
	// HashKey keys the HMAC in placeholders. When it is empty, a random key
	// is made for the process and a warning is logged, because placeholders
	// then change after a restart.
	HashKey []byte
	// Logger receives that warning. Nil means slog.Default().
	Logger *slog.Logger
}

// Stats describe what a redaction did, without any matched value.
type Stats struct {
	// Truncated is set when the result was cut at MaxSize.
	Truncated bool
	// Counts holds the number of matches per rule name. It is nil when
	// nothing was redacted.
	Counts map[string]int
}

// Result is a redacted text.
type Result struct {
	Text string
	Stats
}

// Redactor redacts text and JSON with a fixed set of rules. Build one with
// New at start-up and share it: it is safe for concurrent use.
type Redactor struct {
	rules []rule
	macs  sync.Pool
}

// match is one span of text to replace.
type match struct {
	start, end int
	// rule is the index of the rule in Redactor.rules.
	rule int
}

// Text redacts s. Text longer than MaxSize is cut, and the result is marked
// truncated.
func (r *Redactor) Text(s string) Result {
	truncated := false
	if len(s) > MaxSize+scanMargin {
		s = cutRunes(s, MaxSize+scanMargin)
		truncated = true
	}

	out := r.redact(s, nil)
	if len(out.text) > MaxSize {
		out.text = cutText(out.text, MaxSize)
		truncated = true
	}

	return Result{Text: out.text, Stats: Stats{Truncated: truncated, Counts: out.counts}}
}

// redacted is the output of redact.
type redacted struct {
	text   string
	counts map[string]int
}

// redact replaces every match in s with its placeholder and adds the
// matches to counts, which may be nil.
func (r *Redactor) redact(s string, counts map[string]int) redacted {
	protected := placeholderPattern.FindAllStringIndex(s, -1)
	matches := r.matches(s, protected)

	if len(matches) == 0 {
		return redacted{text: s, counts: counts}
	}

	if counts == nil {
		counts = map[string]int{}
	}

	var b strings.Builder

	b.Grow(len(s))

	last := 0

	for _, m := range matches {
		b.WriteString(s[last:m.start])
		b.WriteString(r.placeholder(m.rule, s[m.start:m.end]))
		counts[r.rules[m.rule].name]++
		last = m.end
	}

	b.WriteString(s[last:])

	return redacted{text: b.String(), counts: counts}
}

// matches returns the spans to replace in s, sorted and without overlaps.
// A span that overlaps a placeholder already in s is dropped. Of two spans
// that overlap, the one that starts first wins; at the same start, the
// rule that comes first, then the longer span.
func (r *Redactor) matches(s string, protected [][]int) []match {
	var all []match

	lower := ""

	for i, rl := range r.rules {
		for _, p := range rl.patterns {
			if len(p.hints) > 0 {
				if lower == "" {
					lower = strings.ToLower(s)
				}

				if !containsAny(lower, p.hints) {
					continue
				}
			}

			all = p.appendMatches(all, s, i)
		}
	}

	slices.SortFunc(all, func(a, b match) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.rule, b.rule), cmp.Compare(b.end, a.end))
	})

	kept := all[:0]
	end := 0

	for _, m := range all {
		if m.start < end || overlapsAny(m, protected) {
			continue
		}

		kept = append(kept, m)
		end = m.end
	}

	return kept
}

// placeholder returns the placeholder for value matched by rule i.
func (r *Redactor) placeholder(i int, value string) string {
	mac := r.macs.Get().(hash.Hash)
	mac.Reset()
	mac.Write([]byte(value))

	var sum [sha256.Size]byte

	digest := mac.Sum(sum[:0])
	r.macs.Put(mac)

	return "[REDACTED:" + r.rules[i].name + ":" + hex.EncodeToString(digest)[:hashChars] + "]"
}

// appendMatches adds the matches of p in s, for rule i, to all.
func (p pattern) appendMatches(all []match, s string, i int) []match {
	var locs [][]int
	if p.group {
		locs = p.re.FindAllStringSubmatchIndex(s, -1)
	} else {
		locs = p.re.FindAllStringIndex(s, -1)
	}

	for _, loc := range locs {
		start, end := loc[0], loc[1]
		if p.group {
			start, end = firstGroup(loc)
		}

		if start < 0 || start == end {
			continue
		}

		if p.valid != nil && !p.valid(s[start:end]) {
			continue
		}

		all = append(all, match{start: start, end: end, rule: i})
	}

	return all
}

// New builds a Redactor from opts. It compiles every rule once and fails on
// an unknown built-in rule name or a custom rule that is not valid.
func New(opts Options) (*Redactor, error) {
	rules, err := selectBuiltIns(opts.BuiltIn)
	if err != nil {
		return nil, fmt.Errorf("built-in rules: %w", err)
	}

	custom, err := compileCustom(opts.Custom)
	if err != nil {
		return nil, fmt.Errorf("custom rules: %w", err)
	}

	key := slices.Clone(opts.HashKey)
	if len(key) == 0 {
		if key, err = processKey(); err != nil {
			return nil, fmt.Errorf("hash key: %w", err)
		}

		cmp.Or(opts.Logger, slog.Default()).Warn("no redaction hash key is set, so placeholders use a random key and change after a restart")
	}

	r := &Redactor{rules: append(rules, custom...)}
	r.macs.New = func() any { return hmac.New(sha256.New, key) }

	return r, nil
}

// selectBuiltIns returns the named built-in rules in priority order, or the
// default ones when names is nil.
func selectBuiltIns(names []string) ([]rule, error) {
	if names == nil {
		names = DefaultRuleNames()
	}

	for _, name := range names {
		if !slices.Contains(BuiltInRuleNames(), name) {
			return nil, fmt.Errorf("rule %q: %w", name, ErrUnknownRule)
		}
	}

	var rules []rule

	for _, r := range builtIns {
		if slices.Contains(names, r.name) {
			rules = append(rules, r)
		}
	}

	return rules, nil
}

// compileCustom compiles the custom rules, in their order.
func compileCustom(custom []Rule) ([]rule, error) {
	rules := make([]rule, 0, len(custom))
	seen := map[string]bool{}

	for i, c := range custom {
		if err := CheckRuleName(c.Name); err != nil {
			return nil, fmt.Errorf("rule %d: %w: %w", i, ErrBadRule, err)
		}

		if seen[c.Name] {
			return nil, fmt.Errorf("rule %q: %w: defined more than once", c.Name, ErrBadRule)
		}

		seen[c.Name] = true

		if c.Pattern == "" {
			return nil, fmt.Errorf("rule %q: %w: empty pattern", c.Name, ErrBadRule)
		}

		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w: %w", c.Name, ErrBadRule, err)
		}

		rules = append(rules, rule{name: c.Name, patterns: []pattern{{re: re}}})
	}

	return rules, nil
}

// firstGroup returns the span of the first capture group that took part in
// a match, or -1, -1 when none did.
func firstGroup(loc []int) (start, end int) {
	for g := 2; g+1 < len(loc); g += 2 {
		if loc[g] >= 0 {
			return loc[g], loc[g+1]
		}
	}

	return -1, -1
}

// containsAny reports whether s holds one of subs.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}

	return false
}

// overlapsAny reports whether m overlaps one of the sorted spans.
func overlapsAny(m match, spans [][]int) bool {
	i, _ := slices.BinarySearchFunc(spans, m.start, func(span []int, start int) int {
		return cmp.Compare(span[1], start+1)
	})

	return i < len(spans) && spans[i][0] < m.end
}

// cutText cuts s to at most limit bytes, at a rune boundary and not inside
// a placeholder.
func cutText(s string, limit int) string {
	cut := len(cutRunes(s, limit))

	lo := max(0, cut-maxPlaceholderLen)
	hi := min(len(s), cut+maxPlaceholderLen)

	for _, loc := range placeholderPattern.FindAllStringIndex(s[lo:hi], -1) {
		if lo+loc[0] < cut && lo+loc[1] > cut {
			cut = lo + loc[0]
		}
	}

	return s[:cut]
}

// cutRunes cuts s to at most limit bytes without splitting a rune.
func cutRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut]
}
