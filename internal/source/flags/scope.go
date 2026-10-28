package flags

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// MaxRange is the longest time range one list_flag_changes call may read.
const MaxRange = 7 * 24 * time.Hour

// Project is one project a flag source may read, with the environments in
// it that it may read.
type Project struct {
	// Key is the project's key in the flag service.
	Key string `yaml:"key"`
	// Environments are the keys of the environments the tools may read.
	Environments []string `yaml:"environments"`
}

// Allowlist is the projects and environments a flag source may read. A
// call for anything else is a tool error, and nothing is sent.
type Allowlist []Project

// Scope is the project and environment one call reads.
type Scope struct {
	Project     string
	Environment string
}

// ChangesInput is the input of list_flag_changes.
type ChangesInput struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Flag        string `json:"flag"`
	Since       string `json:"since"`
	Until       string `json:"until"`
	Limit       int    `json:"limit"`
}

// FlagInput is the input of get_flag.
type FlagInput struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Flag        string `json:"flag"`
}

// Window is the time range of a call: from Start, up to but not including
// End.
type Window struct {
	Start time.Time
	End   time.Time
}

// Check checks the allowlist without any network call: it lists at least
// one project, each with at least one environment, every key matches
// keyPattern, and nothing is listed twice. Each error starts with the key
// path of its field under projects.
func (a Allowlist) Check(keyPattern *regexp.Regexp) error {
	if len(a) == 0 {
		return errors.New("projects: list at least one project, with its environments")
	}

	var errs []error

	seen := map[string]bool{}

	for i, p := range a {
		key := fmt.Sprintf("projects[%d]", i)

		switch {
		case !keyPattern.MatchString(p.Key):
			errs = append(errs, fmt.Errorf("%s.key: %q is not a project key", key, p.Key))
		case seen[p.Key]:
			errs = append(errs, fmt.Errorf("%s.key: %q is listed twice", key, p.Key))
		}

		seen[p.Key] = true

		errs = append(errs, checkEnvironments(key, p.Environments, keyPattern)...)
	}

	return errors.Join(errs...)
}

// Scope returns the project and environment a call names. A project the
// allowlist holds alone, or an environment its project holds alone, may
// be left out. Anything the allowlist does not hold is a
// *source.ToolError that names what is allowed.
func (a Allowlist) Scope(project, environment string) (Scope, error) {
	p, err := a.project(strings.TrimSpace(project))
	if err != nil {
		return Scope{}, fmt.Errorf("read project: %w", err)
	}

	environment = strings.TrimSpace(environment)

	if environment == "" {
		if len(p.Environments) == 1 {
			return Scope{Project: p.Key, Environment: p.Environments[0]}, nil
		}

		return Scope{}, &source.ToolError{Message: fmt.Sprintf("give environment; allowed in project %s: %s",
			p.Key, strings.Join(p.Environments, ", "))}
	}

	if !slices.Contains(p.Environments, environment) {
		return Scope{}, &source.ToolError{Message: fmt.Sprintf("environment %q of project %s is not one Sidekick may read; allowed: %s",
			source.CutText(environment, MaxNameLen), p.Key, strings.Join(p.Environments, ", "))}
	}

	return Scope{Project: p.Key, Environment: environment}, nil
}

// project returns the project of the allowlist a call names.
func (a Allowlist) project(key string) (Project, error) {
	if key == "" {
		if len(a) == 1 {
			return a[0], nil
		}

		return Project{}, &source.ToolError{Message: "give project; allowed: " + strings.Join(a.keys(), ", ")}
	}

	for _, p := range a {
		if p.Key == key {
			return p, nil
		}
	}

	return Project{}, &source.ToolError{Message: fmt.Sprintf("project %q is not one Sidekick may read; allowed: %s",
		source.CutText(key, MaxNameLen), strings.Join(a.keys(), ", "))}
}

// keys returns the project keys of the allowlist.
func (a Allowlist) keys() []string {
	keys := make([]string, 0, len(a))
	for _, p := range a {
		keys = append(keys, p.Key)
	}

	return keys
}

// ParseWindow returns the time range a list_flag_changes call gives: since
// and until as RFC 3339 times, until defaulting to now. since is required.
// A range that is empty or longer than MaxRange is a *source.ToolError.
func ParseWindow(since, until string, now time.Time) (Window, error) {
	if strings.TrimSpace(since) == "" {
		return Window{}, &source.ToolError{Message: "give since, the start of the time range"}
	}

	start, err := parseTime("since", since)
	if err != nil {
		return Window{}, fmt.Errorf("read since: %w", err)
	}

	end := now.UTC()

	if until != "" {
		end, err = parseTime("until", until)
		if err != nil {
			return Window{}, fmt.Errorf("read until: %w", err)
		}
	}

	switch {
	case !start.Before(end):
		return Window{}, &source.ToolError{Message: "since must be before until"}
	case end.Sub(start) > MaxRange:
		return Window{}, &source.ToolError{Message: fmt.Sprintf("the time range is %s, longer than the %s one call may read; move since or until",
			end.Sub(start), MaxRange)}
	}

	return Window{Start: start, End: end}, nil
}

// CheckKey checks a flag key a call gives against keyPattern, the shape of
// keys in the flag service, so it can go into a request path or filter.
func CheckKey(name, key string, keyPattern *regexp.Regexp) error {
	if !keyPattern.MatchString(key) {
		return &source.ToolError{Message: fmt.Sprintf("%s: %q is not a valid key", name, source.CutText(key, MaxNameLen))}
	}

	return nil
}

// Decode decodes a tool's input. The guard has checked it against the
// tool's schema already, so a failure means the schema and the input type
// disagree; it still goes back to the model as a tool error.
func Decode[T any](input json.RawMessage) (T, error) {
	var in T

	if len(input) == 0 {
		return in, nil
	}

	if err := json.Unmarshal(input, &in); err != nil {
		return in, &source.ToolError{Message: "the input does not decode: " + err.Error()}
	}

	return in, nil
}

// Limit returns the number of changes a call asks for, or DefaultChanges,
// within 1 and MaxChanges.
func Limit(limit int) int {
	if limit <= 0 {
		return DefaultChanges
	}

	return min(limit, MaxChanges)
}

// checkEnvironments checks the environments of the project at key.
func checkEnvironments(key string, environments []string, keyPattern *regexp.Regexp) []error {
	if len(environments) == 0 {
		return []error{fmt.Errorf("%s.environments: list at least one environment", key)}
	}

	var errs []error

	seen := map[string]bool{}

	for j, e := range environments {
		envKey := fmt.Sprintf("%s.environments[%d]", key, j)

		switch {
		case !keyPattern.MatchString(e):
			errs = append(errs, fmt.Errorf("%s: %q is not an environment key", envKey, e))
		case seen[e]:
			errs = append(errs, fmt.Errorf("%s: %q is listed twice", envKey, e))
		}

		seen[e] = true
	}

	return errs
}

// parseTime parses an RFC 3339 time, in UTC.
func parseTime(key, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, &source.ToolError{Message: fmt.Sprintf("%s: %q is not an RFC 3339 time such as 2026-05-04T10:00:00Z",
			key, source.CutText(value, MaxNameLen))}
	}

	return t.UTC(), nil
}
