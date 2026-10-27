// Package sinkhttp sends the HTTP requests of outputs that call a plain
// HTTP API, such as ticket comments and webhook callbacks. It waits for a
// short Retry-After on 429 and sends the request again, reads at most
// 1 MiB of a response, and counts failures in
// sidekick_output_api_errors_total.
//
// Server errors and failed requests are returned to the caller, not tried
// again here: the publish job retries them with backoff.
package sinkhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// maxResponseBytes is the most of a response body Do reads.
const maxResponseBytes = 1 << 20

const (
	// maxRateLimitRetries is how many times one call is sent again after a
	// 429 before it fails.
	maxRateLimitRetries = 3
	// defaultRetryAfter is the wait after a 429 without a usable
	// Retry-After header.
	defaultRetryAfter = time.Second
	// maxRetryAfter is the longest Retry-After a call waits for; a longer
	// one fails the call, and the publish job retries it with backoff.
	maxRetryAfter = time.Minute
)

// The label names of sidekick_output_api_errors_total.
const (
	labelType      = "type"
	labelOperation = "operation"
	labelError     = "error"
)

// Reasons a call failed, as sidekick_output_api_errors_total counts them.
// A response with a status outside 2xx is counted as "http_" and the
// status, such as http_503.
const (
	reasonRateLimited   = "rate_limited"
	reasonRequestFailed = "request_failed"
	ReasonBadResponse   = "bad_response"
	ReasonAPIError      = "api_error"
	// reasonHTTPOther counts a status that is not a valid HTTP status.
	reasonHTTPOther = "http_other"
)

// The range of statuses counted by their own number.
const (
	minStatus = 100
	maxStatus = 599
)

// ErrRateLimited means the service kept answering 429 after every retry, or
// asked for a longer wait than a minute.
var ErrRateLimited = errors.New("rate limited")

// errTooLarge is a response body longer than maxResponseBytes.
var errTooLarge = fmt.Errorf("the response is larger than %d bytes", maxResponseBytes)

// Client sends the requests of one output type.
type Client struct {
	outputType string
	http       *http.Client
	errors     *prometheus.CounterVec
	// wait sleeps for d or until ctx ends. Tests replace it.
	wait func(ctx context.Context, d time.Duration) error
}

// Response is a response that was read in full.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// StatusError is a response with a status outside 2xx. It never holds the
// body, which may repeat text the output sent.
type StatusError struct {
	Status int
}

// RequestFunc makes the request of one attempt. Do calls it again for each
// attempt, so the body and any signature are fresh each time.
type RequestFunc func(ctx context.Context) (*http.Request, error)

// Do sends the request newRequest makes and returns its response, whatever
// its status. On 429 it waits for Retry-After and sends a new request, up
// to maxRateLimitRetries times; it returns ErrRateLimited when the service
// keeps refusing or asks for a wait longer than maxRetryAfter. Every
// failure, a status outside 2xx included, is counted under operation.
func (c *Client) Do(ctx context.Context, operation string, newRequest RequestFunc) (Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.send(ctx, newRequest)
		if err != nil {
			c.Failed(operation, reasonRequestFailed)

			return Response{}, fmt.Errorf("%s: %w", operation, err)
		}

		if resp.Status != http.StatusTooManyRequests {
			if !resp.OK() {
				c.Failed(operation, statusReason(resp.Status))
			}

			return resp, nil
		}

		c.Failed(operation, reasonRateLimited)

		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if attempt >= maxRateLimitRetries || retryAfter > maxRetryAfter {
			return Response{}, fmt.Errorf("%s, %d tries: %w", operation, attempt+1, ErrRateLimited)
		}

		if err := c.wait(ctx, retryAfter); err != nil {
			return Response{}, fmt.Errorf("wait %s before retrying %s: %w", retryAfter, operation, err)
		}
	}
}

// Failed counts one failed call of operation for reason, one of the Reason
// constants. Do counts its own failures; callers count the ones they find
// in a response, such as ReasonBadResponse.
func (c *Client) Failed(operation, reason string) {
	if c.errors == nil {
		return
	}

	c.errors.WithLabelValues(c.outputType, operation, reason).Inc()
}

// send makes one request and reads its response.
func (c *Client) send(ctx context.Context, newRequest RequestFunc) (Response, error) {
	req, err := newRequest(ctx)
	if err != nil {
		return Response{}, fmt.Errorf("make the request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("send the request: %w", err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	data, err := io.ReadAll(httpclient.LimitBody(resp.Body, maxResponseBytes, errTooLarge))
	if err != nil {
		return Response{}, fmt.Errorf("read the response: %w", err)
	}

	return Response{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// OK reports whether the status is 2xx.
func (r Response) OK() bool {
	return r.Status >= http.StatusOK && r.Status < http.StatusMultipleChoices
}

// Err returns a *StatusError when the status is not 2xx, or else nil.
func (r Response) Err() error {
	if r.OK() {
		return nil
	}

	return &StatusError{Status: r.Status}
}

// Error names the status.
func (e *StatusError) Error() string {
	return "HTTP status " + strconv.Itoa(e.Status)
}

// New returns a Client for outputType that sends requests with hc, or with
// a client from httpclient.New when hc is nil. It registers
// sidekick_output_api_errors_total, by output type, operation and error,
// with reg, or takes the one another output registered; a nil reg gives a
// client whose failures are not exported.
func New(outputType string, hc *http.Client, reg prometheus.Registerer) (*Client, error) {
	counter, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_output_api_errors_total",
		Help: "Failed calls of outputs to the services they publish to, by output type, operation and error.",
	}, []string{labelType, labelOperation, labelError}))
	if err != nil {
		return nil, fmt.Errorf("register output metrics: %w", err)
	}

	if hc == nil {
		hc = httpclient.New()
	}

	return &Client{outputType: outputType, http: hc, errors: counter, wait: sleep}, nil
}

// statusReason is the error label of a response with status.
func statusReason(status int) string {
	if status < minStatus || status > maxStatus {
		return reasonHTTPOther
	}

	return "http_" + strconv.Itoa(status)
}

// parseRetryAfter reads a Retry-After header, in seconds or as an HTTP
// date. A missing or unusable value gives defaultRetryAfter.
func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}

	if at, err := http.ParseTime(value); err == nil {
		return max(at.Sub(now), 0)
	}

	return defaultRetryAfter
}

// sleep waits for d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait: %w", context.Cause(ctx))
	}
}
