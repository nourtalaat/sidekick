package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// DefaultAPIURL is the base URL of the Slack Web API.
const DefaultAPIURL = "https://slack.com/api/"

// The Web API methods the app calls with the bot token.
const (
	methodReplies = "conversations.replies"
	methodInfo    = "conversations.info"
)

const (
	// maxResponseBytes is the most of a Web API response the app reads.
	maxResponseBytes = 1 << 20
	// channelNameTTL is how long a channel's name is kept before it is
	// looked up again, so a renamed channel is noticed.
	channelNameTTL = 10 * time.Minute
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

var (
	errRateLimited     = errors.New("rate limited by Slack")
	errMessageNotFound = errors.New("the thread's first message was not found")
)

// apiError is a response with "ok": false.
type apiError struct {
	Code string
}

// Error gives Slack's error code; the caller names the method.
func (e *apiError) Error() string {
	return "Slack answered " + e.Code
}

// webAPI calls the Slack Web API methods the app needs with the bot token.
// It does not retry: every call happens before Slack is acknowledged, and a
// failure makes Slack send the event again.
type webAPI struct {
	apiURL string
	http   *http.Client
	token  func(ctx context.Context) (string, error)
	errors *prometheus.CounterVec

	mu       sync.Mutex
	names    map[string]channelName
	now      func() time.Time
	namesTTL time.Duration
}

// channelName is a channel's name as it was looked up.
type channelName struct {
	name    string
	expires time.Time
}

// newWebAPI returns a client for apiURL. Failed calls are counted in
// sidekick_slack_api_errors_total on reg, shared with the Slack output.
func newWebAPI(apiURL string, hc *http.Client, token func(ctx context.Context) (string, error), reg prometheus.Registerer) (*webAPI, error) {
	counter, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_slack_api_errors_total",
		Help: "Failed Slack Web API calls by method and error.",
	}, []string{"method", "error"}))
	if err != nil {
		return nil, fmt.Errorf("register Slack metrics: %w", err)
	}

	return &webAPI{
		apiURL:   apiURL,
		http:     hc,
		token:    token,
		errors:   counter,
		names:    map[string]channelName{},
		now:      time.Now,
		namesTTL: channelNameTTL,
	}, nil
}

// threadParent returns the first message of the thread at threadTS in
// channel, and the message as Slack sent it.
func (c *webAPI) threadParent(ctx context.Context, channel, threadTS string) (message, json.RawMessage, error) {
	var out struct {
		Messages []json.RawMessage `json:"messages"`
	}

	query := url.Values{"channel": {channel}, "ts": {threadTS}, "limit": {"1"}, "inclusive": {"true"}}
	if err := c.call(ctx, methodReplies, query, &out); err != nil {
		return message{}, nil, fmt.Errorf("get the thread's first message: %w", err)
	}

	for _, raw := range out.Messages {
		var m message
		if err := json.Unmarshal(raw, &m); err != nil {
			c.count(methodReplies, errorBadResponse)

			return message{}, nil, fmt.Errorf("decode the thread's first message: %w", err)
		}

		if m.TS == threadTS {
			m.Channel = channel

			return m, raw, nil
		}
	}

	return message{}, nil, errMessageNotFound
}

// channelName returns the name of the channel with id, without "#". Names
// are kept for channelNameTTL.
func (c *webAPI) channelName(ctx context.Context, id string) (string, error) {
	c.mu.Lock()
	cached, ok := c.names[id]
	c.mu.Unlock()

	if ok && c.now().Before(cached.expires) {
		return cached.name, nil
	}

	var out struct {
		Channel struct {
			Name string `json:"name"`
		} `json:"channel"`
	}

	if err := c.call(ctx, methodInfo, url.Values{"channel": {id}}, &out); err != nil {
		return "", fmt.Errorf("get the channel's name: %w", err)
	}

	c.mu.Lock()
	c.names[id] = channelName{name: out.Channel.Name, expires: c.now().Add(c.namesTTL)}
	c.mu.Unlock()

	return out.Channel.Name, nil
}

// call sends a GET to method with query and the bot token, and decodes a
// successful response into out. Every failure is counted.
func (c *webAPI) call(ctx context.Context, method string, query url.Values, out any) error {
	body, err := c.send(ctx, method, query)
	if err != nil {
		return fmt.Errorf("call %s: %w", method, err)
	}

	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}

	if err := json.Unmarshal(body, &env); err != nil {
		c.count(method, errorBadResponse)

		return fmt.Errorf("call %s: decode the response: %w", method, err)
	}

	if !env.OK {
		code := env.Error
		if !errorCodePattern.MatchString(code) {
			code = errorOther
		}

		c.count(method, code)

		return fmt.Errorf("call %s: %w", method, &apiError{Code: code})
	}

	if err := json.Unmarshal(body, out); err != nil {
		c.count(method, errorBadResponse)

		return fmt.Errorf("call %s: decode the response: %w", method, err)
	}

	return nil
}

// send makes one request and returns the body of a 200 response.
func (c *webAPI) send(ctx context.Context, method string, query url.Values) ([]byte, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the bot token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+method+"?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		c.count(method, errorRequestFailed)

		return nil, fmt.Errorf("send the request: %w", err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		c.count(method, errorRateLimited)

		return nil, errRateLimited
	case resp.StatusCode != http.StatusOK:
		c.count(method, errorHTTPStatus)

		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		c.count(method, errorRequestFailed)

		return nil, fmt.Errorf("read the response: %w", err)
	}

	return body, nil
}

// count counts one failed call of method.
func (c *webAPI) count(method, code string) {
	c.errors.WithLabelValues(method, code).Inc()
}
