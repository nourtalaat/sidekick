package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// DefaultAPIURL is the base URL of the Slack Web API.
const DefaultAPIURL = "https://slack.com/api/"

// The Slack Web API methods the sink calls.
const (
	methodPostMessage  = "chat.postMessage"
	methodUpdate       = "chat.update"
	methodGetPermalink = "chat.getPermalink"
)

const (
	// channelRate and channelBurst are the per-channel limit in each
	// replica. Slack allows about one message a second in one channel, with
	// short bursts above it.
	channelRate  = rate.Limit(1)
	channelBurst = 3
	// maxRateLimitRetries is how many times one call is sent again after a
	// 429 before it fails.
	maxRateLimitRetries = 3
	// defaultRetryAfter is the wait after a 429 without a usable
	// Retry-After header.
	defaultRetryAfter = time.Second
	// maxRetryAfter is the longest Retry-After a call waits for; a longer
	// one fails the call, and the publish job retries it with backoff.
	maxRetryAfter = time.Minute
	// maxResponseBytes is the most of a response body the client reads.
	maxResponseBytes = 1 << 20
)

// The error label values for failures that are not a Slack error code.
const (
	errorRateLimited   = "ratelimited"
	errorRequestFailed = "request_failed"
	errorHTTPStatus    = "http_status"
	errorBadResponse   = "bad_response"
	errorOther         = "other"
)

// errorCodePattern is what a Slack error code looks like. Codes that do not
// match are counted as errorOther, so a server cannot add label values.
var errorCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// errRateLimited means Slack kept answering 429 after every retry, or asked
// for a longer wait than maxRetryAfter.
var errRateLimited = errors.New("rate limited by Slack")

// apiError is a response with "ok": false.
type apiError struct {
	Method string
	Code   string
}

// Error names the method and Slack's error code.
func (e *apiError) Error() string {
	return e.Method + ": " + e.Code
}

// client calls the Slack Web API with the bot token. It waits for the
// channel's limiter before each call and honors Retry-After on 429.
type client struct {
	apiURL  string
	http    *http.Client
	token   func(ctx context.Context) (string, error)
	metrics *metrics
	// wait sleeps for d or until ctx ends. Tests replace it.
	wait func(ctx context.Context, d time.Duration) error
	// channelLimit is the rate of each channel limiter. Tests raise it.
	channelLimit rate.Limit

	mu       sync.Mutex
	channels map[string]*rate.Limiter
}

// message is the body of chat.postMessage and chat.update.
type message struct {
	Channel     string       `json:"channel"`
	TS          string       `json:"ts,omitempty"`
	ThreadTS    string       `json:"thread_ts,omitempty"`
	Text        string       `json:"text"`
	Attachments []attachment `json:"attachments"`
	// LinkNames, UnfurlLinks and UnfurlMedia are always false: names in the
	// text are not turned into mentions, and links are not expanded.
	LinkNames   bool `json:"link_names"`
	UnfurlLinks bool `json:"unfurl_links"`
	UnfurlMedia bool `json:"unfurl_media"`
}

// posted is where Slack put a posted or updated message.
type posted struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// envelope is the part every Web API response has.
type envelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// newClient returns a client that sends requests to apiURL with hc.
func newClient(apiURL string, hc *http.Client, token func(ctx context.Context) (string, error), m *metrics) *client {
	return &client{
		apiURL:       apiURL,
		http:         hc,
		token:        token,
		metrics:      m,
		wait:         sleep,
		channelLimit: channelRate,
		channels:     map[string]*rate.Limiter{},
	}
}

// postMessage posts m and returns where it went.
func (c *client) postMessage(ctx context.Context, m message) (posted, error) {
	var out posted
	if err := c.call(ctx, methodPostMessage, m.Channel, jsonRequest(m), &out); err != nil {
		return posted{}, fmt.Errorf("post message: %w", err)
	}

	return out, nil
}

// update replaces the message at m.Channel and m.TS with m.
func (c *client) update(ctx context.Context, m message) (posted, error) {
	var out posted
	if err := c.call(ctx, methodUpdate, m.Channel, jsonRequest(m), &out); err != nil {
		return posted{}, fmt.Errorf("update message: %w", err)
	}

	return out, nil
}

// permalink returns a link to the message at channel and ts.
func (c *client) permalink(ctx context.Context, channel, ts string) (string, error) {
	var out struct {
		Permalink string `json:"permalink"`
	}

	query := url.Values{"channel": {channel}, "message_ts": {ts}}
	if err := c.call(ctx, methodGetPermalink, channel, queryRequest(query), &out); err != nil {
		return "", fmt.Errorf("get permalink: %w", err)
	}

	return out.Permalink, nil
}

// call sends one request to method, after the limiter of channel allows
// it, and decodes the response into out. On 429 it waits for Retry-After
// and sends the request again, up to maxRateLimitRetries times. Every
// failure is counted in sidekick_slack_api_errors_total.
func (c *client) call(ctx context.Context, method, channel string, build requestBuilder, out any) error {
	for attempt := 0; ; attempt++ {
		if err := c.limiter(channel).Wait(ctx); err != nil {
			return fmt.Errorf("wait for the channel limiter: %w", err)
		}

		body, retryAfter, err := c.send(ctx, method, build)
		if err == nil {
			return c.decode(method, body, out)
		}

		if !errors.Is(err, errRateLimited) {
			return err
		}

		c.metrics.apiError(method, errorRateLimited)

		if attempt >= maxRateLimitRetries || retryAfter > maxRetryAfter {
			return fmt.Errorf("%s after %d tries: %w", method, attempt+1, err)
		}

		if err := c.wait(ctx, retryAfter); err != nil {
			return fmt.Errorf("wait %s before retrying %s: %w", retryAfter, method, err)
		}
	}
}

// send makes one HTTP request and returns the body of a 200 response. A
// 429 returns errRateLimited with the wait Slack asked for.
func (c *client) send(ctx context.Context, method string, build requestBuilder) (body []byte, retryAfter time.Duration, err error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("read the bot token: %w", err)
	}

	req, err := build(ctx, c.apiURL+method)
	if err != nil {
		return nil, 0, fmt.Errorf("build the %s request: %w", method, err)
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		c.metrics.apiError(method, errorRequestFailed)

		return nil, 0, fmt.Errorf("send %s: %w", method, err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, parseRetryAfter(resp.Header.Get("Retry-After")), errRateLimited
	}

	if resp.StatusCode != http.StatusOK {
		c.metrics.apiError(method, errorHTTPStatus)

		return nil, 0, fmt.Errorf("%s: HTTP status %d", method, resp.StatusCode)
	}

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		c.metrics.apiError(method, errorRequestFailed)

		return nil, 0, fmt.Errorf("read the %s response: %w", method, err)
	}

	return body, 0, nil
}

// decode reads a 200 response: an error for "ok": false, else the fields
// of out.
func (c *client) decode(method string, body []byte, out any) error {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		c.metrics.apiError(method, errorBadResponse)

		return fmt.Errorf("decode the %s response: %w", method, err)
	}

	if !env.OK {
		code := env.Error
		if !errorCodePattern.MatchString(code) {
			code = errorOther
		}

		c.metrics.apiError(method, code)

		return &apiError{Method: method, Code: code}
	}

	if err := json.Unmarshal(body, out); err != nil {
		c.metrics.apiError(method, errorBadResponse)

		return fmt.Errorf("decode the %s response: %w", method, err)
	}

	return nil
}

// limiter returns the limiter of channel, making it on first use. There is
// one per channel the sink posts to, so the map stays small.
func (c *client) limiter(channel string) *rate.Limiter {
	c.mu.Lock()
	defer c.mu.Unlock()

	l, ok := c.channels[channel]
	if !ok {
		l = rate.NewLimiter(c.channelLimit, channelBurst)
		c.channels[channel] = l
	}

	return l
}

// requestBuilder makes the HTTP request for one call to url.
type requestBuilder func(ctx context.Context, url string) (*http.Request, error)

// jsonRequest posts body as JSON.
func jsonRequest(body any) requestBuilder {
	return func(ctx context.Context, url string) (*http.Request, error) {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode the body: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("make the request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json; charset=utf-8")

		return req, nil
	}
}

// queryRequest sends a GET with query.
func queryRequest(query url.Values) requestBuilder {
	return func(ctx context.Context, url string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"?"+query.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("make the request: %w", err)
		}

		return req, nil
	}
}

// parseRetryAfter reads a Retry-After header in whole seconds, as Slack
// sends it. A missing or unusable value gives defaultRetryAfter.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return defaultRetryAfter
	}

	return time.Duration(seconds) * time.Second
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
