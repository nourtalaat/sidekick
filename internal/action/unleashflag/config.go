package unleashflag

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// The changes the model may propose, as the parameter change and
// limits.allow name them.
const (
	changeTurnOff         = "turn_off"
	changeRestorePrevious = "restore_previous"
)

// knownChanges are the values limits.allow and the parameter change accept.
var knownChanges = []string{changeTurnOff, changeRestorePrevious}

// keyPattern is the shape of an Unleash project, environment or flag key.
// It holds no ':', ',' or '/', so a key can go into a request path or an
// event filter without changing what it names, and it starts with a letter
// or digit, so it is never '.' or '..' in a path.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)

// flagPattern is the shape of an entry of limits.projects[].flags: a flag
// key, in which "*" may stand for any run of characters. "*" is the only
// wildcard: the pattern holds no other character that a glob reads.
var flagPattern = regexp.MustCompile(`^[A-Za-z0-9*][A-Za-z0-9._*-]{0,255}$`)

// Config is the config section of one unleash.flag action. Embedding the
// gate makes the action approval-only.
type Config struct {
	action.ReadWriteGate `yaml:",inline"`

	// BaseURL is the root of the Unleash instance, such as
	// https://unleash.example.com. It may hold a path, for an instance
	// served under one. It must be https, or http on a loopback host.
	// Required.
	BaseURL string `yaml:"baseURL"`
	// TokenSecret holds the service account token Sidekick sends. Give the
	// action a token of its own, with a custom project role that has only
	// the permissions the action uses, apart from the token of any Unleash
	// source.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Limits say which flags the action may change, and how.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of an unleash.flag action. They are checked when
// the model proposes, again right before the change is written, and when it
// is undone.
type Limits struct {
	// Projects are the projects, the environments in each, and the flags in
	// them that the action may change. Everything is required, so nothing is
	// allowed by default.
	Projects []Project `yaml:"projects"`
	// Allow lists the changes the model may propose: turn_off, and
	// restore_previous, which writes more. Empty means turn_off.
	Allow []string `yaml:"allow"`
}

// Project is one project the action may touch.
type Project struct {
	// Key is the project key.
	Key string `yaml:"key"`
	// Environments are the keys of the environments of the project.
	Environments []string `yaml:"environments"`
	// Flags are flag keys, or patterns in which "*" stands for any run of
	// characters, such as checkout-*.
	Flags []string `yaml:"flags"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if err := checkBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("tokenSecret: required"))
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// check returns the problems with the limits.
func (l Limits) check() []error {
	errs := l.checkProjects()

	return append(errs, l.checkAllow()...)
}

// checkProjects returns the problems with limits.projects.
func (l Limits) checkProjects() []error {
	if len(l.Projects) == 0 {
		return []error{errors.New("limits.projects: list at least one project, with its environments and flags")}
	}

	var errs []error

	seen := map[string]bool{}

	for i, p := range l.Projects {
		at := fmt.Sprintf("limits.projects[%d]", i)

		switch {
		case !keyPattern.MatchString(p.Key):
			errs = append(errs, fmt.Errorf("%s.key: %q is not a project key", at, p.Key))
		case seen[p.Key]:
			errs = append(errs, fmt.Errorf("%s.key: %q is listed twice", at, p.Key))
		}

		seen[p.Key] = true

		errs = append(errs, checkList(at+".environments", "environment key", p.Environments, keyPattern)...)
		errs = append(errs, checkList(at+".flags", "flag key or pattern", p.Flags, flagPattern)...)
	}

	return errs
}

// checkAllow returns the problems with limits.allow.
func (l Limits) checkAllow() []error {
	var errs []error

	for i, change := range l.Allow {
		switch {
		case !slices.Contains(knownChanges, change):
			errs = append(errs, fmt.Errorf("limits.allow[%d]: %q is not a change; use %s", i, change, strings.Join(knownChanges, " or ")))
		case slices.Index(l.Allow, change) != i:
			errs = append(errs, fmt.Errorf("limits.allow[%d]: %q is listed twice", i, change))
		}
	}

	return errs
}

// checkList checks that a list has at least one entry, that every entry
// matches pattern, and that none is listed twice.
func checkList(at, what string, list []string, pattern *regexp.Regexp) []error {
	if len(list) == 0 {
		return []error{fmt.Errorf("%s: list at least one", at)}
	}

	var errs []error

	seen := map[string]bool{}

	for i, entry := range list {
		switch {
		case !pattern.MatchString(entry):
			errs = append(errs, fmt.Errorf("%s[%d]: %q is not a %s", at, i, entry, what))
		case seen[entry]:
			errs = append(errs, fmt.Errorf("%s[%d]: %q is listed twice", at, i, entry))
		}

		seen[entry] = true
	}

	return errs
}

// checkBaseURL checks that the base URL is set and is an absolute https
// URL, so the token never crosses the network unencrypted. Plain http is
// allowed only for a loopback host, such as a local test server.
func checkBaseURL(raw string) error {
	if raw == "" {
		return errors.New("baseURL: required, for example https://unleash.example.com")
	}

	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("baseURL: %q must be an absolute https URL, such as https://unleash.example.com", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}
