package gitlab

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// anyDepth is the glob segment that matches any number of path segments.
const anyDepth = "**"

// globSegmentPattern is the shape of one segment of a project glob: the
// characters of a project path, and path.Match wildcards.
var globSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_.*?\[\]^!-]+$`)

// checkProjectGlob checks that pattern is a glob matchGlob can use for a
// project path: group/project with path.Match wildcards in any part, or
// just **. It has no empty segment, and ** is only a whole segment.
func checkProjectGlob(pattern string) error {
	if pattern == "" {
		return errors.New("required")
	}

	segments := strings.Split(pattern, "/")

	for _, seg := range segments {
		switch {
		case seg == "":
			return fmt.Errorf("%q must be a path such as group/project, without an empty part", pattern)
		case strings.Contains(seg, anyDepth) && seg != anyDepth:
			return fmt.Errorf("%q: ** must be a whole part of the path", pattern)
		case seg != anyDepth && !globSegmentPattern.MatchString(seg):
			return fmt.Errorf("%q must be a path such as group/project, with optional wildcards", pattern)
		}

		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("invalid glob %q: %w", pattern, err)
		}
	}

	if len(segments) < 2 && pattern != anyDepth {
		return fmt.Errorf("%q must be a path such as group/project, with optional wildcards", pattern)
	}

	return nil
}

// matchGlob reports whether name matches pattern. The segments of both are
// compared one by one with path.Match, so * and ? never match a slash, and
// a pattern segment of ** matches zero or more segments of name. A pattern
// that checkProjectGlob refuses matches nothing.
//
// This is the same rule the pull request action uses for file paths. It can
// move to a shared package when one exists.
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
