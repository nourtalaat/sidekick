package githubpr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Limits on the validation errors that go back to the model.
const (
	// maxReportedProblems is the most problems listed, over all formats.
	maxReportedProblems = 20
	// maxProblemBytes is the longest message of one problem.
	maxProblemBytes = 300
)

// validationError is the errors the validator found in the files. It wraps
// action.ErrPermanent, because the same files fail again, and its text is
// what the model reads to fix them.
type validationError struct {
	problems []validateclient.Problem
}

// Error lists the problems, one per line.
func (e *validationError) Error() string {
	var b strings.Builder

	b.WriteString("the validator found errors in the files; no pull request is opened until they are fixed:")

	for i, p := range e.problems {
		if i == maxReportedProblems {
			fmt.Fprintf(&b, "\n- and %d more", len(e.problems)-maxReportedProblems)

			break
		}

		b.WriteString("\n- ")
		b.WriteString(p.Path)

		if p.Line > 0 {
			fmt.Fprintf(&b, ":%d", p.Line)
		}

		b.WriteString(": ")
		b.WriteString(oneLine(source.CutText(p.Message, maxProblemBytes)))
	}

	return b.String()
}

// Unwrap lets callers see that repeating the call cannot help.
func (*validationError) Unwrap() error {
	return action.ErrPermanent
}

// validateFiles checks the changes with the validator: files of the rule
// and Grafana formats together, and each changed Terraform module on its
// own. It returns a *validationError when any file fails, listing the
// errors of all of them. A validator that cannot be reached, or that is
// busy, is an error that is tried again.
func (p *PR) validateFiles(ctx context.Context, r *reader, changes []change) error {
	requests := flatRequests(changes)

	modules, err := terraformRequests(ctx, r, changes)
	if err != nil {
		return fmt.Errorf("collect the Terraform files: %w", err)
	}

	var problems []validateclient.Problem

	for _, req := range append(requests, modules...) {
		res, err := p.validator.Validate(ctx, req)
		if err != nil {
			p.metrics.validated(string(req.Format), resultUnavailable)

			if errors.Is(err, validateclient.ErrBadRequest) || errors.Is(err, validateclient.ErrTooLarge) {
				return fmt.Errorf("validate the %s files: %w: %w", req.Format, action.ErrPermanent, err)
			}

			return fmt.Errorf("validate the %s files: %w", req.Format, err)
		}

		if !res.OK {
			p.metrics.validated(string(req.Format), resultFailed)
			problems = append(problems, res.Errors...)

			continue
		}

		p.metrics.validated(string(req.Format), resultPassed)
	}

	if len(problems) > 0 {
		return &validationError{problems: problems}
	}

	return nil
}

// flatRequests makes one request for each format whose files are checked one
// by one: the files of the rule and Grafana formats that the changes write.
func flatRequests(changes []change) []validateclient.Request {
	var requests []validateclient.Request

	for _, format := range []validateclient.Format{validateclient.FormatPrometheusRules, validateclient.FormatGrafanaAlertFile} {
		var files []validateclient.File

		for _, c := range changes {
			if c.format == format && !c.deleted() {
				files = append(files, validateclient.File{Path: c.path, Content: *c.content})
			}
		}

		if len(files) > 0 {
			requests = append(requests, validateclient.Request{Format: format, Files: files})
		}
	}

	return requests
}
