package strategy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxBodyBytes is the largest strategy a runtime save accepts.
const MaxBodyBytes = 64 << 10

// runtimeVersionPrefix starts the version of a runtime strategy, so it
// cannot be mistaken for the hash that built-in and file strategies have.
const runtimeVersionPrefix = "runtime:"

// pathPart matches one step of a problem path: a key or an index.
var pathPart = regexp.MustCompile(`([^.\[\]]+)|\[(\d+)\]`)

// pathOnly matches the text before the first ": " of a message that starts
// with a path, such as watchers[0].check.type.
var pathOnly = regexp.MustCompile(`^[A-Za-z0-9_.\[\]]+$`)

// FieldProblem is one problem in a strategy.
type FieldProblem struct {
	// Path is where in the strategy the problem is, for example
	// match.textPattern or watchers[0].check.type. It is empty for a
	// problem with the whole text, such as a syntax error.
	Path string
	// Line is the line of the problem in the YAML, or zero when it is not
	// known.
	Line int
	// Message says what is wrong.
	Message string
}

// String returns the problem as "path: message", or the message alone when
// it has no path.
func (p FieldProblem) String() string {
	if p.Path == "" {
		return p.Message
	}

	return p.Path + ": " + p.Message
}

// Check decodes and checks the YAML of a runtime strategy with the checks
// file strategies pass, and returns every problem found. The strategy it
// returns has the Runtime origin and no version.
func Check(data []byte) (Strategy, []FieldProblem) {
	if len(data) > MaxBodyBytes {
		return Strategy{}, []FieldProblem{{Message: fmt.Sprintf("the strategy is larger than %d KiB", MaxBodyBytes>>10)}}
	}

	st, problems := parse(data)
	if len(problems) > 0 {
		return Strategy{}, problems
	}

	st.Origin, st.Version = Runtime, ""

	return st, nil
}

// ParseRuntime is Check for the stored version n of a runtime strategy: the
// strategy it returns has the version RuntimeVersion(n).
func ParseRuntime(data []byte, n int) (Strategy, []FieldProblem) {
	st, problems := Check(data)
	if len(problems) > 0 {
		return Strategy{}, problems
	}

	st.Version = RuntimeVersion(n)

	return st, nil
}

// RuntimeVersion returns the version string of version n of a runtime
// strategy, as investigations record it.
func RuntimeVersion(n int) string {
	return runtimeVersionPrefix + strconv.Itoa(n)
}

// RuntimeVersionNumber returns n for the version string runtime:<n>. It
// reports false for any other version, such as a content hash.
func RuntimeVersionNumber(version string) (int, bool) {
	digits, ok := strings.CutPrefix(version, runtimeVersionPrefix)
	if !ok {
		return 0, false
	}

	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 {
		return 0, false
	}

	return n, true
}

// fieldProblems turns the "path: message" strings of validate into
// problems, and finds the line of each path in data.
func fieldProblems(data []byte, messages []string) []FieldProblem {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		root = yaml.Node{}
	}

	out := make([]FieldProblem, len(messages))

	for i, m := range messages {
		out[i] = FieldProblem{Message: m}

		if path, msg, ok := strings.Cut(m, ": "); ok && pathOnly.MatchString(path) {
			out[i] = FieldProblem{Path: path, Message: msg, Line: lineOf(&root, path)}
		}
	}

	return out
}

// lineOf returns the line of path in the document, or of the deepest part
// of it that exists, for example the parent of a missing key. It returns
// zero when no part exists.
func lineOf(root *yaml.Node, path string) int {
	node, line := root, 0
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}

	for _, m := range pathPart.FindAllStringSubmatch(path, -1) {
		next, nextLine := child(node, m[1], m[2])
		if next == nil {
			return line
		}

		node, line = next, nextLine
	}

	return line
}

// child returns the value of key in a mapping, or item index of a
// sequence, with the line of the key or item. It returns nil when node has
// no such part.
func child(node *yaml.Node, key, index string) (*yaml.Node, int) {
	switch {
	case node.Kind == yaml.MappingNode && key != "":
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return node.Content[i+1], node.Content[i].Line
			}
		}
	case node.Kind == yaml.SequenceNode && index != "":
		if n, err := strconv.Atoi(index); err == nil && n < len(node.Content) {
			return node.Content[n], node.Content[n].Line
		}
	}

	return nil, 0
}
