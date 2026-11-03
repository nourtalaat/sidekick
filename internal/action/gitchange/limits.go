package gitchange

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Ceilings for the limits an operator sets, so a config cannot ask for a
// pull request that no person would review.
const (
	// fieldsPerFile is the most fields one file adds to the difference an
	// approver reads: its blob, its status and its two line counts.
	fieldsPerFile = 4
	// MaxFilesCeiling keeps the difference within action.MaxChanges.
	MaxFilesCeiling = action.MaxChanges / fieldsPerFile
	// MaxChangedLinesCeiling is the most lines a config may allow one pull
	// request to add and remove, and so the most a line diff of one of its
	// files holds.
	MaxChangedLinesCeiling = 1000
)

// branchPattern is the shape of a branch name an action accepts: letters,
// digits and . _ - /, where no segment is empty or starts with a dot or a
// hyphen.
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

// knownFormats are the formats a path rule may name.
var knownFormats = []validateclient.Format{
	validateclient.FormatPrometheusRules,
	validateclient.FormatTerraform,
	validateclient.FormatGrafanaAlertFile,
}

// Bounds are the limits that every pull request or merge request action
// shares: how much one change may touch, and the branch it targets. A host
// puts it in its Limits type with the tag `yaml:",inline"`, so the keys are
// maxFiles, maxChangedLines and baseBranch.
type Bounds struct {
	// MaxFiles is the most files one pull request may change.
	MaxFiles int `yaml:"maxFiles"`
	// MaxChangedLines is the most lines one pull request may add or remove
	// in all files together.
	MaxChangedLines int `yaml:"maxChangedLines"`
	// BaseBranch is the only branch a pull request may target.
	BaseBranch string `yaml:"baseBranch"`
}

// PathRule says that files matching Glob are checked as Format.
type PathRule struct {
	// Glob matches a path in the repository: segments are matched with
	// path.Match, and a segment of ** matches any number of segments.
	Glob string `yaml:"glob"`
	// Format is prometheus_rules, terraform or grafana_alert_file.
	Format validateclient.Format `yaml:"format"`
}

// PathRules say which files may change and the format each is checked as.
// The first rule that matches a path decides.
type PathRules []PathRule

// Check returns an error for each limit that is missing or out of range.
// Each error starts with the key of its field.
func (b *Bounds) Check() []error {
	var errs []error

	if b.MaxFiles < 1 || b.MaxFiles > MaxFilesCeiling {
		errs = append(errs, fmt.Errorf("limits.maxFiles: must be from 1 to %d, got %d", MaxFilesCeiling, b.MaxFiles))
	}

	if b.MaxChangedLines < 1 || b.MaxChangedLines > MaxChangedLinesCeiling {
		errs = append(errs, fmt.Errorf("limits.maxChangedLines: must be from 1 to %d, got %d", MaxChangedLinesCeiling, b.MaxChangedLines))
	}

	switch {
	case b.BaseBranch == "":
		errs = append(errs, errors.New("limits.baseBranch: required"))
	case !branchPattern.MatchString(b.BaseBranch) || strings.Contains(b.BaseBranch, ".."):
		errs = append(errs, fmt.Errorf("limits.baseBranch: %q is not a branch name this action accepts", b.BaseBranch))
	}

	return errs
}

// Check checks that there is at least one rule and that each has a valid
// glob and a known format. Each error starts with the key of its field.
func (r PathRules) Check() []error {
	if len(r) == 0 {
		return []error{errors.New("limits.paths: list at least one rule, such as {glob: \"prometheus/rules/**\", format: prometheus_rules}")}
	}

	var errs []error

	for i, rule := range r {
		key := fmt.Sprintf("limits.paths[%d]", i)

		if err := CheckGlob(rule.Glob); err != nil {
			errs = append(errs, fmt.Errorf("%s.glob: %w", key, err))
		}

		switch {
		case rule.Format == "":
			errs = append(errs, fmt.Errorf("%s.format: required; use one of %s", key, formatNames()))
		case !slices.Contains(knownFormats, rule.Format):
			errs = append(errs, fmt.Errorf("%s.format: unknown format %q; use one of %s", key, rule.Format, formatNames()))
		}
	}

	return errs
}

// FormatOf returns the format of the first rule that matches path. When none
// does, the error wraps action.ErrLimit and starts with "matches", so the
// caller can put the path before it.
func (r PathRules) FormatOf(path string) (validateclient.Format, error) {
	for _, rule := range r {
		if matchGlob(rule.Glob, path) {
			return rule.Format, nil
		}
	}

	return "", fmt.Errorf("matches none of the limits.paths globs: %w", action.ErrLimit)
}

// formatNames lists the known formats, for error messages.
func formatNames() string {
	names := make([]string, len(knownFormats))
	for i, f := range knownFormats {
		names[i] = string(f)
	}

	return strings.Join(names, ", ")
}
