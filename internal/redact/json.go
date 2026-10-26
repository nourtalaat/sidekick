package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// errBadToken is returned by the JSON walk for a token where it cannot be,
// which json.Valid should already have ruled out.
var errBadToken = errors.New("unexpected JSON token")

// JSON redacts a JSON payload and returns JSON that still parses.
//
// It redacts every string, object keys included, because maps keyed by an
// email or an IP address are common. A string value whose key names a secret
// (for example "password", "client_secret" or "Authorization") is redacted
// as a whole when the rules find nothing in it on their own. Key order and
// the text of numbers are kept; whitespace between tokens is not.
//
// Input that is not valid JSON is redacted as text and returned as a JSON
// string. A result longer than MaxSize is cut and returned as a JSON string
// that holds the start of the redacted JSON, and marked truncated. Empty
// input is returned as it is.
func (r *Redactor) JSON(raw json.RawMessage) (json.RawMessage, Stats) {
	if len(raw) == 0 {
		return raw, Stats{}
	}

	if !json.Valid(raw) {
		return r.textAsJSON(string(raw))
	}

	w := &jsonWalker{r: r, dec: json.NewDecoder(bytes.NewReader(raw)), out: make([]byte, 0, len(raw))}
	w.dec.UseNumber()

	if err := w.walk(); err != nil {
		// json.Valid accepted raw, so this does not happen; redacting it as
		// text still keeps secrets out.
		return r.textAsJSON(string(raw))
	}

	if len(w.out) <= MaxSize {
		return w.out, Stats{Counts: w.counts}
	}

	return appendJSONString(nil, cutText(string(w.out), MaxSize)), Stats{Truncated: true, Counts: w.counts}
}

// textAsJSON redacts s as text and returns it as a JSON string.
func (r *Redactor) textAsJSON(s string) (json.RawMessage, Stats) {
	res := r.Text(s)

	return appendJSONString(nil, res.Text), res.Stats
}

// jsonWalker copies a JSON document token by token, redacting its strings.
type jsonWalker struct {
	r      *Redactor
	dec    *json.Decoder
	out    []byte
	counts map[string]int
	// stack holds the arrays and objects the walk is inside.
	stack []jsonFrame
}

// jsonFrame is an array or object the walk is inside.
type jsonFrame struct {
	object bool
	// n is the number of elements or members written so far.
	n int
	// key is the key of the member whose value comes next, when wantValue
	// is set.
	key       string
	wantValue bool
}

// walk copies every token of the document to w.out.
func (w *jsonWalker) walk() error {
	for {
		tok, err := w.dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("read JSON token: %w", err)
		}

		if err := w.token(tok); err != nil {
			return fmt.Errorf("write JSON token: %w", err)
		}
	}
}

// token writes one token.
func (w *jsonWalker) token(tok json.Token) error {
	if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
		w.out = utf8.AppendRune(w.out, rune(d))
		w.stack = w.stack[:len(w.stack)-1]
		w.valueDone()

		return nil
	}

	top := w.top()
	if top != nil && top.object && !top.wantValue {
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("object key %v: %w", tok, errBadToken)
		}

		if top.n > 0 {
			w.out = append(w.out, ',')
		}

		w.out = appendJSONString(w.out, w.redact(key))
		w.out = append(w.out, ':')
		top.key, top.wantValue = key, true

		return nil
	}

	if top != nil && !top.object && top.n > 0 {
		w.out = append(w.out, ',')
	}

	switch t := tok.(type) {
	case json.Delim:
		w.out = utf8.AppendRune(w.out, rune(t))
		w.stack = append(w.stack, jsonFrame{object: t == '{'})

		return nil
	case string:
		if top != nil && top.object {
			w.out = appendJSONString(w.out, w.member(top.key, t))
		} else {
			w.out = appendJSONString(w.out, w.redact(t))
		}
	case json.Number:
		w.out = append(w.out, t...)
	case bool:
		w.out = fmt.Append(w.out, t)
	case nil:
		w.out = append(w.out, "null"...)
	default:
		return fmt.Errorf("value %v: %w", tok, errBadToken)
	}

	w.valueDone()

	return nil
}

// member redacts the string value of an object member: with the text
// rules, then, if they found nothing, with the rules that look at the key.
func (w *jsonWalker) member(key, value string) string {
	out := w.r.redact(value, w.counts)
	w.counts = out.counts

	if out.changed {
		return out.text
	}

	for i, rl := range w.r.rules {
		for _, m := range rl.members {
			start, end, ok := m.match(key, value)
			if !ok {
				continue
			}

			if w.counts == nil {
				w.counts = map[string]int{}
			}

			w.counts[rl.name]++

			return value[:start] + w.r.placeholder(i, value[start:end]) + value[end:]
		}
	}

	return value
}

// redact redacts a string with the text rules.
func (w *jsonWalker) redact(s string) string {
	out := w.r.redact(s, w.counts)
	w.counts = out.counts

	return out.text
}

// valueDone records that the current array or object got one more value.
func (w *jsonWalker) valueDone() {
	if top := w.top(); top != nil {
		top.n++
		top.wantValue = false
	}
}

func (w *jsonWalker) top() *jsonFrame {
	if len(w.stack) == 0 {
		return nil
	}

	return &w.stack[len(w.stack)-1]
}

// appendJSONString appends s to dst as a JSON string. Invalid UTF-8 becomes
// U+FFFD. Unlike encoding/json, it leaves <, > and & as they are.
func appendJSONString(dst []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"

	dst = append(dst, '"')

	for i := 0; i < len(s); {
		c := s[i]

		switch {
		case c == '"' || c == '\\':
			dst = append(dst, '\\', c)
			i++
		case c == '\n':
			dst = append(dst, '\\', 'n')
			i++
		case c == '\r':
			dst = append(dst, '\\', 'r')
			i++
		case c == '\t':
			dst = append(dst, '\\', 't')
			i++
		case c < ' ':
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			i++
		case c < utf8.RuneSelf:
			dst = append(dst, c)
			i++
		default:
			ru, size := utf8.DecodeRuneInString(s[i:])
			if ru == utf8.RuneError && size == 1 {
				dst = append(dst, `�`...)
			} else {
				dst = append(dst, s[i:i+size]...)
			}

			i += size
		}
	}

	return append(dst, '"')
}
