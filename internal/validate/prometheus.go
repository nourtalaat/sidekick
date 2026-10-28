package validate

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// promtoolChecker checks Prometheus rules files with promtool check rules,
// one file at a time, so each error names its file.
type promtoolChecker struct {
	promtool string
}

// promtoolPosition is the line and column promtool puts in front of most
// errors, as in "rules.yaml: 5:15: group ...".
var promtoolPosition = regexp.MustCompile(`^(\d+):(\d+): `)

func (c promtoolChecker) check(ctx context.Context, r *run, req Request) (Result, error) {
	var res Result

	for _, f := range req.Files {
		out, err := r.exec(ctx, r.module, nil, c.promtool, "check", "rules", filepath.FromSlash(f.Path))
		if err != nil {
			return Result{}, fmt.Errorf("check %s: %w", f.Path, err)
		}

		if out.exitCode == 0 {
			continue
		}

		res.Errors = append(res.Errors, promtoolProblems(f.Path, out.stdout+"\n"+out.stderr)...)
	}

	return res, nil
}

// promtoolProblems turns the output of a failed promtool check rules into
// problems. Each line that starts with the file's path starts a problem;
// the lines after it, such as the details of a YAML error, belong to it.
// Output with no such line becomes one problem with all of it.
func promtoolProblems(path, output string) []Problem {
	prefix := filepath.FromSlash(path) + ": "

	var problems []Problem

	var rest []string

	for line := range strings.Lines(output) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "", trimmed == "FAILED:", trimmed == "SUCCESS", strings.HasPrefix(trimmed, "Checking "):
			continue
		case strings.HasPrefix(line, prefix):
			problems = append(problems, promtoolProblem(path, strings.TrimPrefix(line, prefix)))
		case len(problems) > 0:
			last := &problems[len(problems)-1]
			last.Message += "; " + trimmed
		default:
			rest = append(rest, trimmed)
		}
	}

	if len(problems) == 0 {
		message := strings.Join(rest, "; ")
		if message == "" {
			message = "promtool check rules failed and printed nothing"
		}

		return []Problem{{Path: path, Message: message}}
	}

	return problems
}

// promtoolProblem makes one problem from an error line without its path,
// taking the line number from its front when there is one.
func promtoolProblem(path, message string) Problem {
	m := promtoolPosition.FindStringSubmatch(message)
	if m == nil {
		return Problem{Path: path, Message: message}
	}

	line, err := strconv.Atoi(m[1])
	if err != nil {
		return Problem{Path: path, Message: message}
	}

	return Problem{Path: path, Line: line, Message: strings.TrimPrefix(message, m[0])}
}
