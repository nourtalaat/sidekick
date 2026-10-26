package watch

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/watch/safehttp"
)

// maxDetailLen is the longest check detail, in bytes.
const maxDetailLen = 512

// Type is the type of a check.
type Type string

// Check types.
const (
	TypeHTTP  Type = "http"
	TypeTool  Type = "tool"
	TypeAlert Type = "alert"
)

// Result is how a check ended.
type Result string

// Results. A pass counts toward recovery; a fail or an error does not.
const (
	// Pass: the service looks up.
	Pass Result = "pass"
	// Fail: the check ran and the service still looks down.
	Fail Result = "fail"
	// Error: the check could not tell, for example because it timed out,
	// the circuit breaker was open or the request was blocked.
	Error Result = "error"
)

// Outcome is the end of one check.
type Outcome struct {
	Result Result
	// Detail is a short summary such as "HTTP 503", redacted and at most
	// 512 bytes. It can hold text from the checked service or source, so it
	// is untrusted: escape it for Slack and wrap it for the model.
	Detail   string
	Duration time.Duration
}

// Check is one check of a watcher: an HTTPCheck, a ToolCheck or an
// AlertCheck.
type Check interface {
	// Type returns the check's type.
	Type() Type
	// Validate checks the check's own settings. Checker.Validate also
	// checks them against the deployment, such as the allowed hosts.
	Validate() error

	run(ctx context.Context, c *Checker, target Target) Outcome
}

// Target is what a check runs for.
type Target struct {
	// InvestigationID is the investigation of the watcher, passed to
	// sources with tool calls.
	InvestigationID string
	// GroupID is the event group of the watcher, whose alerts an alert
	// check reads.
	GroupID uuid.UUID
}

// Options configure a Checker.
type Options struct {
	// Guard runs tool checks and HTTP checks with the source limits.
	// Required.
	Guard *tools.Guard
	// HTTP is the client of HTTP checks. Required.
	HTTP *safehttp.Client
	// Redactor redacts every detail. Required.
	Redactor *redact.Redactor
	// Secrets resolves the header values of HTTP checks, on each check so
	// rotated secrets apply at once. Nil fails HTTP checks with headers.
	Secrets registry.Secrets
	// Alerts reads the alerts of a group for alert checks. Nil fails alert
	// checks.
	Alerts AlertStates
	// Metrics records the check metrics. Nil records none.
	Metrics *Metrics
}

// errMissingOption is returned by New without a required option.
var errMissingOption = errors.New("new watch checker: Guard, HTTP and Redactor are required")

// Checker runs checks. Build one at start-up and share it: it is safe for
// concurrent use.
type Checker struct {
	opts Options
}

// Run runs one check for target and returns how it ended. It never fails:
// a check that cannot run ends with the result Error. ctx bounds the whole
// check.
func (c *Checker) Run(ctx context.Context, target Target, check Check) Outcome {
	start := time.Now()

	o := check.run(ctx, c, target)
	o.Detail = c.detail(o.Detail)
	o.Duration = time.Since(start)

	c.opts.Metrics.recordCheck(check.Type(), o.Result, o.Duration)

	return o
}

// Validate checks a check's settings, and for an HTTP check that the
// client takes its URL: an http or https URL on an allowed host. Each
// problem is one line of the error, starting with the field it is about.
func (c *Checker) Validate(check Check) error {
	var hostErr error

	if h, ok := check.(HTTPCheck); ok && h.URL != "" {
		if err := c.opts.HTTP.Check(h.URL); err != nil && safehttp.CheckURL(h.URL) == nil {
			hostErr = fmt.Errorf("url: %w", err)
		}
	}

	return errors.Join(check.Validate(), hostErr)
}

// detail redacts a detail and cuts it to maxDetailLen bytes, on one line.
func (c *Checker) detail(s string) string {
	s = strings.Join(strings.Fields(s), " ")

	return source.CutText(c.opts.Redactor.Text(s).Text, maxDetailLen)
}

// New builds a checker.
func New(opts Options) (*Checker, error) {
	if opts.Guard == nil || opts.HTTP == nil || opts.Redactor == nil {
		return nil, errMissingOption
	}

	return &Checker{opts: opts}, nil
}

// errorOutcome returns an Error outcome with the detail.
func errorOutcome(detail string) Outcome {
	return Outcome{Result: Error, Detail: detail}
}

// sortedKeys returns the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
