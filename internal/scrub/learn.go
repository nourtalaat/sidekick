package scrub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/nourtalaat/sidekick/internal/redact"
)

// entityKey identifies a learned value.
type entityKey struct {
	kind  Kind
	value string
}

// entity is a learned value and its placeholder.
type entity struct {
	kind        Kind
	value       string
	placeholder string
	// aliases are other spellings of the same value: the words of a
	// person's name.
	aliases []string
}

// learnScalar learns what a string or a keyed number holds.
func (s *Session) learnScalar(v scalar) {
	if v.node.Tag == "!!str" {
		s.learnText(v.node.Value, v.kind, 0)

		return
	}

	s.learn(v.kind, v.node.Value)
}

// learnText learns every value in text. kind is the kind of value the text
// is as a whole, when the key above it says so.
func (s *Session) learnText(text string, kind Kind, depth int) {
	if depth >= maxJSONDepth || strings.TrimSpace(text) == "" {
		return
	}

	if kind != "" {
		s.learnKeyed(kind, text)
	}

	s.learnJSON(text, depth)
	s.learnURLs(text, depth)
	s.learnSpans(text)
	s.learnHosts(text)
	s.learnPairs(text)

	if strings.Contains(text, `\"`) {
		s.learnPairs(strings.ReplaceAll(text, `\"`, `"`))
	}

	for _, m := range arnPattern.FindAllStringSubmatch(text, -1) {
		s.learn(KindAccount, m[1])
	}
}

// learnKeyed learns v as a value of kind.
func (s *Session) learnKeyed(kind Kind, v string) {
	if kind == KindHost {
		s.learnHost(v)

		return
	}

	s.learn(kind, v)
}

// learnHost learns v as an IP address or a host name.
func (s *Session) learnHost(v string) {
	v = strings.Trim(strings.ToLower(strings.TrimSpace(v)), "[]")

	if isIP(v) {
		s.learn(KindIP, v)

		return
	}

	s.learn(KindHost, v)
}

// learnJSON learns from text that is JSON, or from each of its lines that is.
func (s *Session) learnJSON(text string, depth int) {
	trimmed := strings.TrimSpace(text)

	if looksLikeJSON(trimmed) && s.learnDocument(trimmed, depth) {
		return
	}

	if !strings.Contains(text, "\n") {
		return
	}

	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if looksLikeJSON(line) {
			s.learnDocument(line, depth)
		}
	}
}

// learnDocument learns from a JSON document and reports whether text was one.
func (s *Session) learnDocument(text string, depth int) bool {
	if !json.Valid([]byte(text)) {
		return false
	}

	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return false
	}

	s.learnValue(v, "", depth+1)

	return true
}

// learnValue walks a decoded JSON value. key is the member name above it.
func (s *Session) learnValue(v any, key string, depth int) {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		for _, k := range keys {
			s.learnText(k, "", depth)
			s.learnValue(v[k], k, depth)
		}
	case []any:
		for _, item := range v {
			s.learnValue(item, key, depth)
		}
	case string:
		s.learnText(v, kindOfKey(key), depth)
	case json.Number:
		if kind := kindOfKey(key); kind != "" {
			s.learn(kind, v.String())
		}
	}
}

// learnURLs learns the host, the user name and the query values of every
// URL in text.
func (s *Session) learnURLs(text string, depth int) {
	for _, raw := range urlPattern.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, ".,;:!?)]}'\"")

		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}

		s.learnHost(u.Hostname())

		if u.User != nil {
			s.learn(KindUser, u.User.Username())
		}

		query := u.Query()
		for _, k := range slices.Sorted(maps.Keys(query)) {
			for _, v := range query[k] {
				s.learnText(v, kindOfKey(k), depth+1)
			}
		}
	}
}

// learnSpans learns the emails and IP addresses the built-in rules find.
func (s *Session) learnSpans(text string) {
	for _, sp := range s.c.finder.Spans(text) {
		if sp.Rule == redact.Emails {
			s.learn(KindUser, text[sp.Start:sp.End])

			continue
		}

		s.learn(KindIP, text[sp.Start:sp.End])
	}
}

// learnHosts learns every dotted name that ends in a known domain suffix.
func (s *Session) learnHosts(text string) {
	for _, m := range hostPattern.FindAllString(text, -1) {
		if hasHostSuffix(m) {
			s.learn(KindHost, m)
		}
	}
}

// learnPairs learns the value of every key=value, key: value and
// "key":"value" whose key names a kind of value.
func (s *Session) learnPairs(text string) {
	for _, m := range pairPattern.FindAllStringSubmatch(text, -1) {
		kind := kindOfKey(m[1])
		if kind == "" {
			continue
		}

		for _, v := range m[2:] {
			if v != "" {
				s.learnKeyed(kind, v)

				break
			}
		}
	}
}

// learn adds a value of kind, if it is one, and gives it the next
// placeholder of its kind.
func (s *Session) learn(kind Kind, v string) {
	value := normalize(kind, v)
	if value == "" {
		return
	}

	key := entityKey{kind: kind, value: value}
	if _, ok := s.entities[key]; ok {
		return
	}

	s.counts[kind]++

	e := &entity{kind: kind, value: value, placeholder: fmt.Sprintf("%s-%d", kind, s.counts[kind])}

	if kind == KindUser && strings.Contains(value, " ") {
		e.aliases = nameWords(value)
	}

	s.entities[key] = e
	s.order = append(s.order, e)
	s.matcher = nil
}

// nameWords returns the words of a name that are long enough to stand
// for the person.
func nameWords(name string) []string {
	var words []string

	for _, w := range strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len(w) > minValueLen && !slices.Contains(genericValues, w) {
			words = append(words, w)
		}
	}

	return words
}

// looksLikeJSON reports whether text starts like a JSON object or array.
func looksLikeJSON(text string) bool {
	return strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")
}
