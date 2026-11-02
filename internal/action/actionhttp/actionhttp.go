// Package actionhttp holds the HTTP steps that the actions calling a web
// service share: sending a request and reading a size-limited answer, and
// deciding from the status whether a failed call is worth repeating. It
// knows nothing about any one service.
package actionhttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// MaxRetryAfter is the longest Retry-After value Response keeps.
const MaxRetryAfter = time.Minute

// ErrTooLarge is returned when a service answers with more than the limit.
var ErrTooLarge = errors.New("the service answered with more than the action reads")

// Response is a service's answer: its status and its body, read up to the
// limit given to Do.
type Response struct {
	Status int
	Body   []byte
	// RetryAfter is the wait the service asked for, cut to MaxRetryAfter;
	// zero when it asked for none.
	RetryAfter time.Duration
}

// StatusError is a status outside 2xx. It has no body on purpose: a service
// can repeat the text of a request in its error answer.
type StatusError struct {
	Status int
}

// Error implements error.
func (e *StatusError) Error() string {
	return "the service answered with status " + strconv.Itoa(e.Status)
}

// Do sends req with client and reads at most maxBytes of the answer. A
// transport failure or an answer longer than maxBytes is an error that
// does not wrap action.ErrPermanent, so the proposal is tried again.
func Do(client *http.Client, req *http.Request, maxBytes int64) (Response, error) {
	resp, err := client.Do(req) //nolint:gosec // G704: the caller builds req from the URL in the action's config, which CheckURL checked.
	if err != nil {
		return Response{}, fmt.Errorf("send the request: %w", err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(httpclient.LimitBody(resp.Body, maxBytes, ErrTooLarge))
	if err != nil {
		return Response{}, fmt.Errorf("read the answer: %w", err)
	}

	return Response{Status: resp.StatusCode, Body: body, RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}, nil
}

// Err returns nil for a 2xx status. For a timeout, a rate limit or a server
// error it returns a *StatusError that Sidekick tries again. For any other
// status it returns a *StatusError that also wraps action.ErrPermanent,
// since repeating the call cannot change the answer.
func (r Response) Err() error {
	switch {
	case r.Status >= http.StatusOK && r.Status < http.StatusMultipleChoices:
		return nil
	case Retryable(r.Status):
		return &StatusError{Status: r.Status}
	default:
		return fmt.Errorf("%w: %w", action.ErrPermanent, &StatusError{Status: r.Status})
	}
}

// Retryable reports whether a call that ended with status may pass when
// repeated: a timeout, a rate limit or a server error.
func Retryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// retryAfter reads a Retry-After header given in seconds.
func retryAfter(value string) time.Duration {
	secs, err := strconv.Atoi(value)
	if err != nil || secs <= 0 {
		return 0
	}

	return min(time.Duration(secs)*time.Second, MaxRetryAfter)
}

// CheckURL checks that raw is an absolute https URL, so credentials never
// cross the network unencrypted. Plain http is allowed only for a loopback
// host, such as a local test server. A user name or password in the URL is
// refused, because the URL shows up in config listings and logs. key names
// the config field in the error.
func CheckURL(key, raw string) error {
	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return fmt.Errorf("%s: must be an absolute https URL", key)
	case u.User != nil:
		return fmt.Errorf("%s: must not hold a user name or password; use the action's secret instead", key)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("%s: must use https; plain http is only allowed for localhost", key)
	default:
		return nil
	}
}
