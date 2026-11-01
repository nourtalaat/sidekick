package action

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on the parameters of a proposal.
const (
	// MaxParamsBytes is the longest parameters a proposal may have, as the
	// model wrote them.
	MaxParamsBytes = 64 << 10
	// maxParamsDepth is how deeply parameters may nest.
	maxParamsDepth = 32
	// hexDigits is the digits of a \u escape.
	hexDigits = "0123456789abcdef"
	// maxBMP is the last code point a single \u escape holds.
	maxBMP = 0xFFFF
	// surrogateOffset and the two surrogate bases split a code point above
	// maxBMP into two \u escapes.
	surrogateOffset = 0x10000
	highSurrogate   = 0xD800
	lowSurrogate    = 0xDC00
	surrogateBits   = 10
	surrogateMask   = 0x3FF
)

// ErrParams is wrapped by every problem with the form of the parameters.
var ErrParams = errors.New("invalid parameters")

// CanonicalParams checks the parameters the model wrote and returns them in
// the one form that is stored, shown and run: a JSON object that is valid
// UTF-8, at most MaxParamsBytes long, nested at most 32 levels, with no key
// twice in an object, and with its keys sorted and its whitespace and
// escapes written the same way every time. Without the check, a key given
// twice could read one way to an approver and another way to the action.
func CanonicalParams(raw json.RawMessage) (json.RawMessage, error) {
	switch {
	case len(raw) > MaxParamsBytes:
		return nil, fmt.Errorf("%w: %d bytes, at most %d", ErrParams, len(raw), MaxParamsBytes)
	case !utf8.Valid(raw) || !json.Valid(raw):
		return nil, fmt.Errorf("%w: not valid JSON text", ErrParams)
	}

	if first := strings.TrimLeft(string(raw), " \t\r\n"); !strings.HasPrefix(first, "{") {
		return nil, fmt.Errorf("%w: must be a JSON object", ErrParams)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	if err := checkValue(dec, 0); err != nil {
		return nil, fmt.Errorf("check the keys and nesting: %w", err)
	}

	var value any

	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParams, err)
	}

	out, err := encode(value, "")
	if err != nil {
		return nil, fmt.Errorf("encode the parameters: %w", err)
	}

	return out, nil
}

// DisplayParams returns the text an approver reads for the stored
// parameters: indented JSON in which every character that could hide or
// change what the text says is written as a \u escape. That covers control
// characters, the characters that reorder or join text, spaces other than
// the plain space, and the backtick, which could close a code block. A
// newline inside a value shows as \n, so a value cannot push other lines
// out of sight. Nothing is cut: the text holds every parameter.
func DisplayParams(params json.RawMessage) string {
	var value any

	dec := json.NewDecoder(bytes.NewReader(params))
	dec.UseNumber()

	if err := dec.Decode(&value); err != nil {
		return spell(string(params))
	}

	out, err := encode(value, "  ")
	if err != nil {
		return spell(string(params))
	}

	return spell(string(out))
}

// checkValue reads one value from dec and fails for an object that has a
// key twice or a value nested deeper than maxParamsDepth.
func checkValue(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrParams, err)
	}

	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}

	if depth >= maxParamsDepth {
		return fmt.Errorf("%w: nested more than %d levels deep", ErrParams, maxParamsDepth)
	}

	seen := map[string]bool{}

	for dec.More() {
		if delim == '{' {
			keyTok, err := dec.Token()
			if err != nil {
				return fmt.Errorf("%w: %w", ErrParams, err)
			}

			key, _ := keyTok.(string)
			if seen[key] {
				return fmt.Errorf("%w: the key %q is given twice", ErrParams, key)
			}

			seen[key] = true
		}

		if err := checkValue(dec, depth+1); err != nil {
			return err
		}
	}

	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("%w: %w", ErrParams, err)
	}

	return nil
}

// encode writes value as JSON, without escaping <, > and &, with indent
// when it is not empty.
func encode(value any, indent string) (json.RawMessage, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if indent != "" {
		enc.SetIndent("", indent)
	}

	if err := enc.Encode(value); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// spell writes the characters of s that could hide something as \u escapes.
func spell(s string) string {
	var b strings.Builder

	for _, r := range s {
		if r == '\n' || !needsSpelling(r) {
			b.WriteRune(r)

			continue
		}

		if r > maxBMP {
			r -= surrogateOffset
			writeEscape(&b, highSurrogate+(r>>surrogateBits))
			writeEscape(&b, lowSurrogate+(r&surrogateMask))

			continue
		}

		writeEscape(&b, r)
	}

	return b.String()
}

// needsSpelling reports whether r is shown as a \u escape.
func needsSpelling(r rune) bool {
	switch r {
	case '`', utf8.RuneError:
		return true
	case ' ':
		return false
	default:
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zs, unicode.Zl, unicode.Zp, unicode.Co, unicode.Cs) || !unicode.IsPrint(r)
	}
}

// writeEscape writes r, below 0x10000, as \uXXXX.
func writeEscape(b *strings.Builder, r rune) {
	b.WriteString(`\u`)

	for shift := 12; shift >= 0; shift -= 4 {
		b.WriteByte(hexDigits[(r>>shift)&0xF])
	}
}
