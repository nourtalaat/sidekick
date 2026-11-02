package githubpr

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// anyDepth is the glob segment that matches any number of path segments.
const anyDepth = "**"

// checkGlob checks that pattern is a glob matchGlob can use: not empty, not
// absolute, with valid path.Match segments, and with ** only as a whole
// segment.
func checkGlob(pattern string) error {
	if pattern == "" {
		return errors.New("required")
	}

	if strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("%q must be a path from the root of the repository, without a leading slash", pattern)
	}

	for seg := range strings.SplitSeq(pattern, "/") {
		if seg == "" {
			return fmt.Errorf("%q has an empty path segment", pattern)
		}

		if strings.Contains(seg, anyDepth) && seg != anyDepth {
			return fmt.Errorf("%q: ** must be a whole path segment", pattern)
		}

		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("%q is not a valid glob: %w", pattern, err)
		}
	}

	return nil
}

// matchGlob reports whether name matches pattern. The segments of both are
// compared one by one with path.Match, so * and ? never match a slash, and a
// pattern segment of ** matches zero or more segments of name. A pattern
// that checkGlob refuses matches nothing.
func matchGlob(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// matchSegments matches the segments of a name against those of a pattern.
func matchSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == anyDepth {
			rest := pattern[1:]

			for skip := 0; skip <= len(name); skip++ {
				if matchSegments(rest, name[skip:]) {
					return true
				}
			}

			return false
		}

		if len(name) == 0 {
			return false
		}

		if ok, err := path.Match(pattern[0], name[0]); err != nil || !ok {
			return false
		}

		pattern, name = pattern[1:], name[1:]
	}

	return len(name) == 0
}
