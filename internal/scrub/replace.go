package scrub

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// matcher finds every learned value in a text.
type matcher struct {
	re *regexp.Regexp
	// placeholders maps a lower-cased spelling of a value to its placeholder.
	placeholders map[string]string
}

// replace returns text with every learned value replaced by its
// placeholder. A value only counts when it stands alone: "bob" is not
// found in "bobcat" or in "web-bob-2". Text inside a redaction placeholder
// is left alone.
func (s *Session) replace(text string) string {
	m := s.currentMatcher()
	if m == nil || text == "" {
		return text
	}

	protected := redactedToken.FindAllStringIndex(text, -1)

	var b strings.Builder

	last, pos := 0, 0

	for pos < len(text) {
		loc := m.re.FindStringIndex(text[pos:])
		if loc == nil {
			break
		}

		start, end := pos+loc[0], pos+loc[1]

		placeholder, ok := m.placeholders[strings.ToLower(text[start:end])]
		if ok && standsAlone(text, start, end) && !insideSpan(protected, start) {
			b.WriteString(text[last:start])
			b.WriteString(placeholder)

			last, pos = end, end

			continue
		}

		_, size := utf8.DecodeRuneInString(text[start:])
		pos = start + size
	}

	b.WriteString(text[last:])

	return b.String()
}

// currentMatcher builds the matcher of the learned values, or returns nil
// when there are none.
func (s *Session) currentMatcher() *matcher {
	if len(s.order) == 0 {
		return nil
	}

	if s.matcher != nil {
		return s.matcher
	}

	placeholders := map[string]string{}

	for _, e := range s.order {
		for _, n := range e.needles() {
			if _, taken := placeholders[n]; !taken {
				placeholders[n] = e.placeholder
			}
		}
	}

	needles := make([]string, 0, len(placeholders))
	for n := range placeholders {
		needles = append(needles, n)
	}

	slices.SortFunc(needles, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}

		return strings.Compare(a, b)
	})

	quoted := make([]string, len(needles))
	for i, n := range needles {
		quoted[i] = regexp.QuoteMeta(n)
	}

	s.matcher = &matcher{re: regexp.MustCompile("(?i)" + strings.Join(quoted, "|")), placeholders: placeholders}

	return s.matcher
}

// needles returns the lower-cased spellings of the entity that are looked
// for: the value, how a URL query writes it, and its aliases.
func (e *entity) needles() []string {
	out := []string{e.value}

	if q := strings.ToLower(url.QueryEscape(e.value)); q != e.value {
		out = append(out, q)
	}

	return append(out, e.aliases...)
}

// standsAlone reports whether text[start:end] is a whole word: no letter,
// digit or underscore touches it, and no hyphen that joins it to another
// word.
func standsAlone(text string, start, end int) bool {
	if start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if isWord(r) {
			return false
		}

		if r == '-' && start-size > 0 {
			before, _ := utf8.DecodeLastRuneInString(text[:start-size])
			if isWord(before) {
				return false
			}
		}
	}

	if end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if isWord(r) {
			return false
		}

		if r == '-' && end+size < len(text) {
			after, _ := utf8.DecodeRuneInString(text[end+size:])
			if isWord(after) {
				return false
			}
		}
	}

	return true
}

// isWord reports whether r is a letter, a digit or an underscore.
func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// insideSpan reports whether pos is inside one of the sorted spans.
func insideSpan(spans [][]int, pos int) bool {
	return slices.ContainsFunc(spans, func(s []int) bool { return s[0] <= pos && pos < s[1] })
}
