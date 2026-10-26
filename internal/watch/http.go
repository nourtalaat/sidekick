package watch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/watch/safehttp"
)

// Settings of HTTP checks.
const (
	// DefaultCheckTimeout bounds an HTTP check whose Timeout is zero.
	DefaultCheckTimeout = 10 * time.Second
	// httpConcurrency is how many HTTP checks run at once against one
	// host, in each replica.
	httpConcurrency = 2
	// httpKeyPrefix starts the guard key of a host.
	httpKeyPrefix = "http:"
	// maxBodyExcerpt is how much of a response body a failed check's
	// detail shows, in bytes.
	maxBodyExcerpt = 160
)

// HTTP status codes a check can expect.
const (
	minStatus = 100
	maxStatus = 599
)

// headersCheckDoesNotSend are headers an HTTP check may not set: the client
// sends no cookies, and the host comes from the URL.
var headersCheckDoesNotSend = []string{"Cookie", "Host"}

// headerNamePattern is the form of an HTTP header name.
var headerNamePattern = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

// HTTPCheck passes when a GET of URL returns one of the expected statuses
// and, when BodyRegex is set, a body that matches it. The request goes
// through the safe HTTP client, on an allowed host, and through the guard
// with at most 2 checks at once per host.
type HTTPCheck struct {
	URL    string `yaml:"url" json:"url"`
	Expect Expect `yaml:"expect" json:"expect,omitzero"`
	// Headers are sent with the request. Their values come only from
	// secrets, read on each check.
	Headers map[string]registry.SecretRef `yaml:"headers" json:"headers,omitempty"`
	// Timeout bounds the request. Zero means DefaultCheckTimeout.
	Timeout time.Duration `yaml:"timeout" json:"timeout,omitempty"`
}

// Expect is what a passing HTTP check gets.
type Expect struct {
	// Status are the passing statuses. Empty means 200 only.
	Status []int `yaml:"status" json:"status,omitempty"`
	// BodyRegex must match the body, up to safehttp.MaxBody bytes, when set.
	BodyRegex string `yaml:"bodyRegex" json:"bodyRegex,omitempty"`
}

// Type returns TypeHTTP.
func (HTTPCheck) Type() Type {
	return TypeHTTP
}

// Validate checks the URL's form, the statuses, the body regex, the header
// names and the timeout. Checker.Validate also checks the host.
func (h HTTPCheck) Validate() error {
	var errs []error

	if h.URL == "" {
		errs = append(errs, errors.New("url: required"))
	} else if err := safehttp.CheckURL(h.URL); err != nil {
		errs = append(errs, fmt.Errorf("url: %w", err))
	}

	for i, s := range h.Expect.Status {
		if s < minStatus || s > maxStatus {
			errs = append(errs, fmt.Errorf("expect.status[%d]: %d is not an HTTP status", i, s))
		}
	}

	if _, err := regexp.Compile(h.Expect.BodyRegex); err != nil {
		errs = append(errs, fmt.Errorf("expect.bodyRegex: %w", err))
	}

	for _, name := range sortedKeys(h.Headers) {
		switch {
		case !headerNamePattern.MatchString(name):
			errs = append(errs, fmt.Errorf("headers.%s: not a valid header name", name))
		case slices.Contains(headersCheckDoesNotSend, http.CanonicalHeaderKey(name)):
			errs = append(errs, fmt.Errorf("headers.%s: HTTP checks do not send this header", name))
		}
	}

	if h.Timeout < 0 {
		errs = append(errs, errors.New("timeout: must not be negative"))
	}

	return errors.Join(errs...)
}

// run sends the request through the guard and judges the response.
func (h HTTPCheck) run(ctx context.Context, c *Checker, _ Target) Outcome {
	u, err := url.Parse(h.URL)
	if err != nil {
		return errorOutcome("the URL does not parse")
	}

	header, err := c.headers(ctx, h.Headers)
	if err != nil {
		return errorOutcome(err.Error())
	}

	re, err := regexp.Compile(h.Expect.BodyRegex)
	if err != nil {
		return errorOutcome("expect.bodyRegex does not compile")
	}

	var (
		resp   *safehttp.Response
		getErr error
	)

	terr := c.opts.Guard.Do(ctx, httpKeyPrefix+strings.ToLower(u.Hostname()), httpConcurrency, h.timeout(), func(ctx context.Context) error {
		resp, getErr = c.opts.HTTP.Get(ctx, h.URL, header)
		if _, blocked := errors.AsType[*safehttp.BlockedError](getErr); blocked || getErr == nil {
			return nil
		}

		return fmt.Errorf("HTTP check: %w", getErr)
	})

	if blocked, ok := errors.AsType[*safehttp.BlockedError](getErr); ok {
		return errorOutcome("blocked: " + blocked.Message)
	}

	if terr != nil {
		return errorOutcome(describeGuardError(terr))
	}

	return h.judge(c, resp, re)
}

// judge turns a response into an outcome.
func (h HTTPCheck) judge(c *Checker, resp *safehttp.Response, re *regexp.Regexp) Outcome {
	summary := fmt.Sprintf("HTTP %d", resp.Status)
	if resp.NotFollowed != "" {
		summary += ", " + resp.NotFollowed
	}

	if !slices.Contains(h.expectedStatus(), resp.Status) {
		return Outcome{Result: Fail, Detail: withExcerpt(summary, c.excerpt(resp.Body))}
	}

	if h.Expect.BodyRegex != "" && !re.Match(resp.Body) {
		return Outcome{Result: Fail, Detail: withExcerpt(summary+", the body does not match expect.bodyRegex", c.excerpt(resp.Body))}
	}

	return Outcome{Result: Pass, Detail: summary}
}

// timeout returns the check's timeout, or the default.
func (h HTTPCheck) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}

	return DefaultCheckTimeout
}

// expectedStatus returns the passing statuses, or 200.
func (h HTTPCheck) expectedStatus() []int {
	if len(h.Expect.Status) > 0 {
		return h.Expect.Status
	}

	return []int{http.StatusOK}
}

// headers resolves the header values from their secrets.
func (c *Checker) headers(ctx context.Context, refs map[string]registry.SecretRef) (http.Header, error) {
	header := http.Header{}
	if len(refs) == 0 {
		return header, nil
	}

	if c.opts.Secrets == nil {
		return nil, errors.New("HTTP check headers need secrets, and none are set up")
	}

	for _, name := range sortedKeys(refs) {
		value, err := secrets.ResolveText(ctx, c.opts.Secrets, refs[name])
		if err != nil {
			return nil, fmt.Errorf("cannot read the secret %s/%s of header %s: %w", refs[name].Name, refs[name].Key, name, err)
		}

		header.Set(name, value)
	}

	return header, nil
}

// excerpt returns the start of a body for a detail: redacted as a whole
// first, so a cut cannot split a secret and leave part of it, then cut.
func (c *Checker) excerpt(body []byte) string {
	text := strings.Join(strings.Fields(c.opts.Redactor.Text(string(body)).Text), " ")
	if len(text) > maxBodyExcerpt {
		return source.CutText(text, maxBodyExcerpt) + "..."
	}

	return text
}

// withExcerpt adds a body excerpt to a summary.
func withExcerpt(summary, excerpt string) string {
	if excerpt == "" {
		return summary
	}

	return summary + ": " + excerpt
}

// describeGuardError describes a failed guarded call for a detail.
func describeGuardError(terr *tools.ToolError) string {
	switch terr.Kind {
	case tools.KindTimeout:
		return "timeout: " + terr.Message
	case tools.KindUnavailable:
		return "skipped: " + terr.Message
	case tools.KindInvalidInput, tools.KindUnknownTool, tools.KindToolError, tools.KindFailed:
		return "failed: " + terr.Message
	default:
		return "failed: " + terr.Message
	}
}
