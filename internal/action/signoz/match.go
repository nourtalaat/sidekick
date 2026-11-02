package signoz

import "strings"

// nameMatches reports whether name matches pattern. The only wildcard is
// "*", which stands for any run of characters, also none. The pattern must
// match the whole name, and the match is case sensitive.
func nameMatches(pattern, name string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == name
	}

	head, tail := parts[0], parts[len(parts)-1]
	if !strings.HasPrefix(name, head) {
		return false
	}

	rest := name[len(head):]

	for _, middle := range parts[1 : len(parts)-1] {
		at := strings.Index(rest, middle)
		if at < 0 {
			return false
		}

		rest = rest[at+len(middle):]
	}

	return strings.HasSuffix(rest, tail)
}

// matchesAnyName reports whether name matches one of the patterns.
func matchesAnyName(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if nameMatches(pattern, name) {
			return true
		}
	}

	return false
}
