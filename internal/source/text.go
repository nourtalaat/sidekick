package source

import "unicode/utf8"

// CutText cuts s to at most n bytes without splitting a UTF-8 character,
// for sources and the guard that limit the size of text they keep.
func CutText(s string, n int) string {
	if len(s) <= n {
		return s
	}

	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}

	return s[:n]
}
