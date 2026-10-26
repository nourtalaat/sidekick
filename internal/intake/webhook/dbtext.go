package webhook

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// Postgres text and jsonb cannot hold the NUL character, and text must be
// valid UTF-8, so payload text is fixed up before it is stored.
const (
	replacementChar = "�"
	nulEscape       = `\u0000`
	replacedEscape  = `�`
)

// dbText returns s with invalid UTF-8 and NUL characters replaced by
// U+FFFD.
func dbText(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, replacementChar), "\x00", replacementChar)
}

// validText reports whether a text column can hold s as it is.
func validText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// dbJSON returns the valid JSON raw with every \u0000 escape inside its
// strings replaced by �, which jsonb accepts. In valid JSON a
// backslash only appears inside a string and always starts an escape, so
// skipping the character after each one finds every escape.
func dbJSON(raw []byte) []byte {
	if !bytes.Contains(raw, []byte(nulEscape)) {
		return raw
	}

	out := make([]byte, 0, len(raw))

	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 >= len(raw) {
			out = append(out, raw[i])

			continue
		}

		if bytes.HasPrefix(raw[i:], []byte(nulEscape)) {
			out = append(out, replacedEscape...)
			i += len(nulEscape) - 1

			continue
		}

		out = append(out, raw[i], raw[i+1])
		i++
	}

	return out
}
