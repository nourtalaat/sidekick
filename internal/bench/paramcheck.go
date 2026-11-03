package bench

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// anyElement is the index of a path step written [*]: any element.
const anyElement = -1

// pathStepPattern is one step of a param check path: a key, then any
// number of [n] or [*] indexes.
var pathStepPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)((?:\[(?:[0-9]+|\*)\])*)$`)

// indexPattern finds each index of a path step.
var indexPattern = regexp.MustCompile(`\[([0-9]+|\*)\]`)

// pathStep is one step of a param check path: an object key and the
// indexes that follow it, each an array position or anyElement.
type pathStep struct {
	key     string
	indexes []int
}

// parsePath reads a param check path such as files[*].path: keys joined
// with ".", each followed by [n] for the array element n, counting from 0,
// or [*] for any element.
func parsePath(path string) ([]pathStep, error) {
	if path == "" {
		return nil, errors.New("the path is empty")
	}

	parts := strings.Split(path, ".")
	steps := make([]pathStep, 0, len(parts))

	for _, part := range parts {
		m := pathStepPattern.FindStringSubmatch(part)
		if m == nil {
			return nil, fmt.Errorf("%q is not a key with optional [n] or [*] indexes", part)
		}

		step := pathStep{key: m[1]}

		for _, idx := range indexPattern.FindAllStringSubmatch(m[2], -1) {
			if idx[1] == "*" {
				step.indexes = append(step.indexes, anyElement)

				continue
			}

			n, err := strconv.Atoi(idx[1])
			if err != nil {
				return nil, fmt.Errorf("index %s of %q is too large", idx[1], part)
			}

			step.indexes = append(step.indexes, n)
		}

		steps = append(steps, step)
	}

	return steps, nil
}

// hasWildcard reports whether any step of the path has a [*] index.
func hasWildcard(steps []pathStep) bool {
	for _, s := range steps {
		for _, i := range s.indexes {
			if i == anyElement {
				return true
			}
		}
	}

	return false
}
