package grafana

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

// maxUIDLength is the longest UID Grafana accepts for a rule or a folder.
const maxUIDLength = 40

// uidShape is what Grafana accepts as a UID: letters, digits, "-" and "_".
var uidShape = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// editableRoots are the top-level fields of a rule that limits.fields may
// name. The others are set by Grafana (id, orgID, uid, updated, provenance),
// move the rule (folderUID, ruleGroup), or are not supported by this action
// (record, notification_settings, missingSeriesEvalsToResolve).
var editableRoots = []string{"title", "condition", "data", "noDataState", "execErrState", "for", "keep_firing_for", "annotations", "labels", "isPaused"}

// Config is the config section of one grafana.alert_rule action.
type Config struct {
	// URL is the root of Grafana, such as https://acme.grafana.net. It may
	// have a path when Grafana is served from a sub-path. It must be https,
	// or http on a loopback host.
	URL string `yaml:"url"`
	// TokenSecret holds a Grafana service account token. Give the action a
	// token of its own, whose service account can edit alert rules in the
	// allowed folders only, apart from the token of any source that reads
	// Grafana.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Limits say which rules the action may change and which fields.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a grafana.alert_rule action. They are checked
// when the model proposes, again right before the change is written, and
// when it is undone.
type Limits struct {
	// FolderUIDs are the folders whose rules the action may change.
	FolderUIDs []string `yaml:"folderUids"`
	// RuleUIDs narrow the folders to these rules.
	RuleUIDs []string `yaml:"ruleUids"`
	// TitlePatterns narrow the folders to the rules whose title matches one
	// of them. In a pattern "*" stands for any text and everything else
	// stands for itself; a pattern matches the whole title, and case counts.
	// A rule is allowed when its UID is in RuleUIDs or its title matches a
	// pattern. With neither list set, every rule in the folders is allowed.
	TitlePatterns []string `yaml:"titlePatterns"`
	// Fields are the fields of a rule the action may change, as paths such
	// as for or data[0].model.conditions. A path allows the fields below it
	// too.
	Fields []string `yaml:"fields"`
	// AllowDelete lets the model propose to delete a rule. A delete cannot
	// be undone in Sidekick. The default is false.
	AllowDelete bool `yaml:"allowDelete"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if err := checkURL(c.URL); err != nil {
		errs = append(errs, err)
	}

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("tokenSecret: required"))
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// apiURL returns the root of Grafana, without a trailing slash.
func (c *Config) apiURL() string {
	return strings.TrimSuffix(c.URL, "/")
}

// check returns the problems with the limits.
func (l Limits) check() []error {
	var errs []error

	if len(l.FolderUIDs) == 0 {
		errs = append(errs, errors.New("limits.folderUids: at least one folder UID is required"))
	}

	errs = append(errs, checkUIDs("limits.folderUids", l.FolderUIDs)...)
	errs = append(errs, checkUIDs("limits.ruleUids", l.RuleUIDs)...)

	if _, err := compilePatterns(l.TitlePatterns); err != nil {
		errs = append(errs, err)
	}

	if len(l.Fields) == 0 && !l.AllowDelete {
		errs = append(errs, errors.New("limits.fields: at least one field is required"))
	}

	for _, field := range l.Fields {
		if err := checkField(field); err != nil {
			errs = append(errs, fmt.Errorf("limits.fields: %w", err))
		}
	}

	return errs
}

// ruleAllowed reports whether the narrowing lists of the limits allow a
// rule with this UID and title. The folder is checked apart.
func (l Limits) ruleAllowed(uid, title string, patterns []*regexp.Regexp) bool {
	if len(l.RuleUIDs) == 0 && len(patterns) == 0 {
		return true
	}

	if slices.Contains(l.RuleUIDs, uid) {
		return true
	}

	return slices.ContainsFunc(patterns, func(p *regexp.Regexp) bool { return p.MatchString(title) })
}

// checkUIDs checks that every entry of a list of UIDs is one Grafana accepts.
func checkUIDs(key string, uids []string) []error {
	var errs []error

	for _, uid := range uids {
		if !validUID(uid) {
			errs = append(errs, fmt.Errorf("%s: %q must be 1 to %d letters, digits, \"-\" or \"_\"", key, uid, maxUIDLength))
		}
	}

	return errs
}

// validUID reports whether uid has the shape of a Grafana UID.
func validUID(uid string) bool {
	return len(uid) <= maxUIDLength && uidShape.MatchString(uid)
}

// compilePatterns turns each title pattern into an expression that matches
// the whole title. "*" matches any text, newlines included.
func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))

	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "" {
			return nil, errors.New("limits.titlePatterns: a pattern must not be empty")
		}

		expr := "(?s)^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"

		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("limits.titlePatterns: %q: %w", pattern, err)
		}

		compiled = append(compiled, re)
	}

	return compiled, nil
}

// checkField checks one entry of limits.fields: a valid path whose first
// key is a field Grafana lets people change through this action.
func checkField(field string) error {
	path, err := action.ParsePath(field)
	if err != nil {
		return fmt.Errorf("check the path: %w", err)
	}

	if !slices.Contains(editableRoots, path[0]) {
		return fmt.Errorf("%q: the first key must be one of %s", field, strings.Join(editableRoots, ", "))
	}

	return nil
}

// checkURL checks that the URL is absolute and keeps the token from crossing
// the network unencrypted: https, or http on a loopback host.
func checkURL(raw string) error {
	if raw == "" {
		return errors.New("url: required, such as https://acme.grafana.net")
	}

	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("url: %q must be an absolute https URL, such as https://acme.grafana.net", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("url: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}
