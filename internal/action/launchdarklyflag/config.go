package launchdarklyflag

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// keyPattern is the shape of a LaunchDarkly project or environment key. It
// holds no '*', ':', ';', ',' or '/', so a key can go into a request path
// or a resource specifier without changing what it names.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)

// flagPattern is the shape of an entry of limits.projects[].flags: a flag
// key, in which "*" may stand for any run of characters. "*" is the only
// wildcard: the pattern holds no other character that a glob reads.
var flagPattern = regexp.MustCompile(`^[A-Za-z0-9*][A-Za-z0-9._*-]{0,255}$`)

// Config is the config section of one launchdarkly.flag action. Embedding
// the gate makes the action approval-only.
type Config struct {
	action.ReadWriteGate `yaml:",inline"`

	// TokenSecret holds the access token Sidekick sends. Give the action a
	// token of its own, with a custom role that can do only what the action
	// does, apart from the token of any LaunchDarkly source.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// BaseURL is the root of the LaunchDarkly instance, for the EU or
	// Federal instance. Empty means https://app.launchdarkly.com. It must
	// be https, or http on a loopback host.
	BaseURL string `yaml:"baseURL"`
	// Limits say which flags the action may change.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a launchdarkly.flag action. They are checked
// when the model proposes, again right before the change is written, and
// when it is undone.
type Limits struct {
	// Projects are the projects, the environments in each, and the flags in
	// them that the action may change. Everything is required, so nothing is
	// allowed by default.
	Projects []Project `yaml:"projects"`
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

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("tokenSecret: required"))
	}

	if err := checkBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// check returns the problems with the limits.
func (l Limits) check() []error {
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

// checkBaseURL checks that the base URL, when set, is an absolute https
// URL, so the token never crosses the network unencrypted. Plain http is
// allowed only for a loopback host, such as a local test server.
func checkBaseURL(raw string) error {
	if raw == "" {
		return nil
	}

	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("baseURL: %q must be an absolute https URL, such as https://app.eu.launchdarkly.com", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}
