package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Defaults of the retention periods that are not set in the file.
const (
	DefaultEventsRetention         = 90 * Day
	DefaultInvestigationsRetention = 365 * Day
	DefaultStepsRetention          = 90 * Day
	DefaultAuditLogRetention       = 730 * Day
	DefaultWatcherChecksRetention  = 30 * Day
)

// Day is 24 hours, the unit of the d suffix in retention periods.
const Day = 24 * time.Hour

// retentionPath is the path of the retention section.
const retentionPath = "retention"

// errBadPeriod is the problem of a period that does not parse. The decoder
// puts the field path and the value around it.
var errBadPeriod = errors.New("expected a period such as 90d or 36h")

// Retention says how long Sidekick keeps each kind of record before the
// daily retention job deletes it. Only the file sets it.
type Retention struct {
	// Events are the received alerts and messages, by when they arrived.
	Events Period `yaml:"events"`
	// Investigations are the investigations, by when they were created.
	// Deleting one also deletes its steps, inbox, output refs, publish
	// state, feedback, watchers and event group.
	Investigations Period `yaml:"investigations"`
	// Steps are the full step records of investigation runs, by when each
	// step started. The report stays with the investigation.
	Steps Period `yaml:"steps"`
	// AuditLog are the audit log entries, by when they were written.
	AuditLog Period `yaml:"auditLog"`
	// WatcherChecks are the results of single watcher checks, by when each
	// ran. The watcher itself stays with the investigation.
	WatcherChecks Period `yaml:"watcherChecks"`
}

// Period is a retention period: a whole number of days such as 90d, or a
// Go duration such as 36h.
type Period time.Duration

// Duration returns the period as a time.Duration.
func (p Period) Duration() time.Duration {
	return time.Duration(p)
}

// String returns the period in days when it is a whole number of days,
// and as a Go duration otherwise.
func (p Period) String() string {
	d := p.Duration()
	if d != 0 && d%Day == 0 {
		return strconv.FormatInt(int64(d/Day), 10) + "d"
	}

	return d.String()
}

// UnmarshalYAML reads a period such as 90d or 36h.
func (p *Period) UnmarshalYAML(node *yaml.Node) error {
	d, err := parsePeriod(node.Value)
	if node.Kind != yaml.ScalarNode || err != nil {
		return errBadPeriod
	}

	*p = Period(d)

	return nil
}

func (r *Retention) setDefaults() {
	r.Events = Period(DefaultEventsRetention)
	r.Investigations = Period(DefaultInvestigationsRetention)
	r.Steps = Period(DefaultStepsRetention)
	r.AuditLog = Period(DefaultAuditLogRetention)
	r.WatcherChecks = Period(DefaultWatcherChecksRetention)
}

// checkRetention checks that every period is positive, and that steps and
// watcher checks are not kept longer than the investigations they belong
// to, which take them along when they are deleted.
func (v *validator) checkRetention() {
	r := v.cfg.Retention

	periods := []namedPeriod{
		{"events", r.Events},
		{"investigations", r.Investigations},
		{"steps", r.Steps},
		{"auditLog", r.AuditLog},
		{"watcherChecks", r.WatcherChecks},
	}

	for _, p := range periods {
		v.positiveDuration(joinPath(retentionPath, p.key), p.period.Duration())
	}

	if r.Investigations <= 0 {
		return
	}

	for _, p := range []namedPeriod{{"steps", r.Steps}, {"watcherChecks", r.WatcherChecks}} {
		if p.period > r.Investigations {
			v.errs.add(joinPath(retentionPath, p.key), "%s is longer than %s.investigations %s; they are deleted with their investigation",
				p.period, retentionPath, r.Investigations)
		}
	}
}

// namedPeriod is a retention period with its key in the section.
type namedPeriod struct {
	key    string
	period Period
}

// parsePeriod parses a whole number of days with the suffix d, or a Go
// duration.
func parsePeriod(s string) (time.Duration, error) {
	days, ok := strings.CutSuffix(s, "d")
	if !ok {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("parse duration %q: %w", s, err)
		}

		return d, nil
	}

	n, err := strconv.ParseInt(days, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse days %q: %w", s, err)
	}

	return time.Duration(n) * Day, nil
}
