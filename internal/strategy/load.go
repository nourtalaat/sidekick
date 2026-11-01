package strategy

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// versionLength is how many hex characters of the content's SHA-256 a
// version keeps: 64 bits, plenty to tell the versions of one strategy apart.
const versionLength = 16

// Extensions of the files read as strategies.
var strategyExtensions = []string{".yaml", ".yml"}

var (
	// yamlUnknownField matches the YAML library's message for an unknown
	// key, which strict decoding turns into an error.
	yamlUnknownField = regexp.MustCompile(`field (\S+) not found in type \S+`)
	// yamlPrefix matches what the YAML library puts before its messages.
	yamlPrefix = regexp.MustCompile(`^yaml: (unmarshal errors:\n)?`)
)

// Set holds the strategies a deployment can use, by name. Load makes it
// from the built-in strategies with the file strategies on top. WithRuntime
// adds the strategies saved at runtime between the two: runtime over
// built-in, file over runtime. A Set never changes after it is made.
type Set struct {
	// byName holds the strategy in effect for each name.
	byName map[string]Strategy
	// builtIn, runtime and file hold the strategies of each layer.
	builtIn, runtime, file map[string]Strategy
}

// Problem is one problem in one strategy file.
type Problem struct {
	// File is the path of the file, or the file name of a built-in one.
	File    string
	Message string
}

// String returns the problem as "file: message".
func (p Problem) String() string {
	return p.File + ": " + p.Message
}

// Errors lists every problem found while loading strategies, one per line
// in Error, in file order.
type Errors []Problem

// Error implements error.
func (e Errors) Error() string {
	lines := make([]string, len(e))
	for i, p := range e {
		lines[i] = p.String()
	}

	return strings.Join(lines, "\n")
}

// Get returns the strategy with the given name. A nil set holds none.
func (s *Set) Get(name string) (Strategy, bool) {
	if s == nil {
		return Strategy{}, false
	}

	st, ok := s.byName[name]

	return st, ok
}

// Names returns the names of every strategy, sorted. A nil set holds none.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}

	return slices.Sorted(maps.Keys(s.byName))
}

// Load reads the built-in strategies from builtIn and the file strategies
// from dir, and returns them as one set. Only *.yaml and *.yml files at the
// top of each are read; hidden files are skipped. An empty dir, or one that
// does not exist, adds no file strategies. A file strategy with the name of
// a built-in one replaces it, and Load logs a line saying so. Problems in
// the files are returned together as Errors; a dir that cannot be read is
// returned as a plain error. A nil logger logs nothing.
func Load(builtIn fs.FS, dir string, logger *slog.Logger) (*Set, error) {
	logger = cmp.Or(logger, slog.New(slog.DiscardHandler))

	builtIns, problems, err := loadFS(builtIn, BuiltIn, func(name string) string { return name })
	if err != nil {
		return nil, fmt.Errorf("read built-in strategies: %w", err)
	}

	files, fileProblems, err := loadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("load file strategies: %w", err)
	}

	problems = append(problems, fileProblems...)
	if len(problems) > 0 {
		return nil, problems
	}

	set := &Set{builtIn: byName(builtIns), file: byName(files)}
	set.byName = effective(set.builtIn, nil, set.file)

	for _, st := range files {
		if old, ok := set.builtIn[st.Name]; ok {
			logger.Info("file strategy overrides built-in strategy",
				slog.String("strategy", st.Name),
				slog.String("file", st.Path),
				slog.String("builtInVersion", old.Version),
				slog.String("version", st.Version))
		}
	}

	return set, nil
}

// loadDir reads the file strategies in dir.
func loadDir(dir string) ([]Strategy, Errors, error) {
	if dir == "" {
		return nil, nil, nil
	}

	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}

	files, problems, err := loadFS(os.DirFS(dir), File, func(name string) string { return filepath.Join(dir, name) })
	if err != nil {
		return nil, nil, fmt.Errorf("read strategies dir %s: %w", dir, err)
	}

	return files, problems, nil
}

// loadFS reads every strategy file at the top of fsys. pathOf turns a file
// name into the path shown in problems and in Strategy.Path.
func loadFS(fsys fs.FS, origin Origin, pathOf func(string) string) ([]Strategy, Errors, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("list files: %w", err)
	}

	var (
		out      []Strategy
		problems Errors
	)

	seen := map[string]string{}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !slices.Contains(strategyExtensions, path.Ext(name)) {
			continue
		}

		p := pathOf(name)

		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			problems = append(problems, Problem{File: p, Message: "cannot read: " + err.Error()})

			continue
		}

		st, found := parse(data)
		if len(found) > 0 {
			for _, f := range found {
				problems = append(problems, Problem{File: p, Message: f.String()})
			}

			continue
		}

		if first, ok := seen[st.Name]; ok {
			problems = append(problems, Problem{File: p, Message: fmt.Sprintf("strategy %q is already defined in %s", st.Name, first)})

			continue
		}

		seen[st.Name] = p
		st.Origin = origin
		st.Path = p
		out = append(out, st)
	}

	return out, problems, nil
}

// parse decodes and checks one strategy file, and sets its version and
// body. It returns every problem found.
func parse(data []byte) (Strategy, []FieldProblem) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var st Strategy
	if err := dec.Decode(&st); err != nil {
		if errors.Is(err, io.EOF) {
			return Strategy{}, []FieldProblem{{Message: "the file is empty"}}
		}

		return Strategy{}, yamlProblems(err)
	}

	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Strategy{}, []FieldProblem{{Message: "the file must hold one YAML document"}}
	}

	if found := st.validate(); len(found) > 0 {
		return Strategy{}, fieldProblems(data, found)
	}

	sum := sha256.Sum256(data)
	st.Version = hex.EncodeToString(sum[:])[:versionLength]
	st.Body = string(data)

	return st, nil
}

// yamlProblems splits a YAML library error into one problem per message,
// and names unknown keys plainly.
func yamlProblems(err error) []FieldProblem {
	msg := yamlPrefix.ReplaceAllString(err.Error(), "")

	var out []FieldProblem

	for line := range strings.SplitSeq(msg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		out = append(out, FieldProblem{Message: yamlUnknownField.ReplaceAllString(line, `unknown key "$1"`)})
	}

	return out
}
