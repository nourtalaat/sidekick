package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	bot "github.com/nourtalaat/sidekick/internal/teams"
)

// DefaultServiceURL is the Bot Framework service URL of the public cloud,
// for posts that answer no Teams message.
const DefaultServiceURL = "https://smba.trafficmanager.net/teams/"

// The operations of the Bot Framework Connector the sink calls, as the
// metrics name them.
const (
	opStartThread = "create_conversation"
	opReply       = "send_to_conversation"
	opUpdate      = "update_activity"
)

const (
	// conversationRate and conversationBurst are the limit of each thread
	// in each replica, well under the 7 operations a second Teams allows
	// a bot in one conversation.
	conversationRate  = rate.Limit(2)
	conversationBurst = 4
	// maxRetries is how many times one call is sent again after an answer
	// Teams asks bots to retry: 429, 412, 502 and 504.
	maxRetries = 3
	// firstBackoff is the wait before the first retry when Teams sends no
	// usable Retry-After; it doubles each time.
	firstBackoff = time.Second
	// maxRetryAfter is the longest wait a call accepts; a longer one fails
	// the call, and the publish job retries it with backoff.
	maxRetryAfter = time.Minute
	// maxResponseBytes is the most of a response body the client reads.
	maxResponseBytes = 1 << 20
)

// The status label values for failures that have no HTTP status.
const (
	statusRequestFailed = "request_failed"
	statusBadResponse   = "bad_response"
)

// The hosts the Bot Framework's service URLs are on. The bot's token is
// sent only to them, to the configured service URL's host and to loopback
// hosts, so a bad service URL cannot leak it. Only the public cloud's host
// is allowed under trafficmanager.net, because anyone can make a Traffic
// Manager name there; the other domains belong to Microsoft.
var (
	serviceHosts        = []string{"smba.trafficmanager.net"}
	serviceHostSuffixes = []string{".botframework.com", ".teams.microsoft.com", ".teams.microsoft.us"}
)

var (
	// errNotFound means Teams no longer has the message, usually because
	// someone deleted it.
	errNotFound   = errors.New("the message is gone")
	errRetryLater = errors.New("retry later, as Teams asked")
	errNoID       = errors.New("teams returned no activity ID")
	errTooLarge   = errors.New("the response is larger than 1 MiB")
)

// client calls the Bot Framework Connector with the bot's token. It waits
// for the conversation's limiter before each call, retries the answers
// Teams asks bots to retry, and sends a call once more with a new token
// after a 401.
type client struct {
	http    *http.Client
	tokens  *bot.Tokens
	metrics *metrics
	// allowedHost is the host of the configured service URL.
	allowedHost string
	// wait sleeps for d or until ctx ends. Tests replace it.
	wait func(ctx context.Context, d time.Duration) error
	// limit is the rate of each conversation limiter. Tests raise it.
	limit rate.Limit

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// activity is a message the bot sends: a card, with a summary that Teams
// shows in notifications.
type activity struct {
	Type        string           `json:"type"`
	Summary     string           `json:"summary,omitempty"`
	Attachments []cardAttachment `json:"attachments"`
}

// cardAttachment carries one Adaptive Card.
type cardAttachment struct {
	ContentType string `json:"contentType"`
	Content     card   `json:"content"`
}

// newThread is the body that starts a new thread in a channel.
type newThread struct {
	IsGroup     bool          `json:"isGroup"`
	Activity    activity      `json:"activity"`
	ChannelData threadChannel `json:"channelData"`
	TenantID    string        `json:"tenantId"`
}

// threadChannel names the channel and tenant of a new thread.
type threadChannel struct {
	Channel idRef `json:"channel"`
	Tenant  idRef `json:"tenant"`
}

// idRef is an object with an ID.
type idRef struct {
	ID string `json:"id"`
}

// startThread posts a as the first message of a new thread in channel and
// returns its ID, which is also the thread's.
func (c *client) startThread(ctx context.Context, serviceURL, channel, tenant string, a activity) (string, error) {
	body := newThread{IsGroup: true, Activity: a, ChannelData: threadChannel{Channel: idRef{ID: channel}, Tenant: idRef{ID: tenant}}, TenantID: tenant}

	var out struct {
		ActivityID string `json:"activityId"`
	}

	if err := c.call(ctx, opStartThread, http.MethodPost, serviceURL, "v3/conversations", channel, body, &out); err != nil {
		return "", fmt.Errorf("start a thread: %w", err)
	}

	if out.ActivityID == "" {
		return "", errNoID
	}

	return out.ActivityID, nil
}

// reply posts a in conversation, the conversation ID of a channel thread,
// and returns its ID.
func (c *client) reply(ctx context.Context, serviceURL, conversation string, a activity) (string, error) {
	var out idRef

	if err := c.call(ctx, opReply, http.MethodPost, serviceURL, "v3/conversations/"+url.PathEscape(conversation)+"/activities", conversation, a, &out); err != nil {
		return "", fmt.Errorf("reply in the thread: %w", err)
	}

	if out.ID == "" {
		return "", errNoID
	}

	return out.ID, nil
}

// update replaces the message id in conversation with a. An error that
// wraps errNotFound means the message is gone.
func (c *client) update(ctx context.Context, serviceURL, conversation, id string, a activity) error {
	path := "v3/conversations/" + url.PathEscape(conversation) + "/activities/" + url.PathEscape(id)

	if err := c.call(ctx, opUpdate, http.MethodPut, serviceURL, path, conversation, a, nil); err != nil {
		return fmt.Errorf("update the message: %w", err)
	}

	return nil
}

// call sends body to path under serviceURL, after the limiter of key
// allows it, and decodes the answer into out unless out is nil. Every
// failure is counted in sidekick_teams_api_errors_total.
func (c *client) call(ctx context.Context, op, method, serviceURL, path, key string, body, out any) error {
	if err := c.checkServiceURL(serviceURL); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the body: %w", err)
	}

	target := strings.TrimSuffix(serviceURL, "/") + "/" + path
	refreshed := false

	for attempt := 0; ; attempt++ {
		if err := c.limiter(key).Wait(ctx); err != nil {
			return fmt.Errorf("wait for the conversation limiter: %w", err)
		}

		resp, token, err := c.send(ctx, method, target, data)
		if err != nil {
			c.metrics.apiError(op, statusRequestFailed)

			return fmt.Errorf("%s: %w", op, err)
		}

		retryAfter, err := c.answer(op, resp, out)

		switch {
		case err == nil:
			return nil
		case resp.status == http.StatusUnauthorized && !refreshed:
			c.tokens.Invalidate(token)

			refreshed = true

			continue
		case !errors.Is(err, errRetryLater):
			return fmt.Errorf("%s: %w", op, err)
		case attempt >= maxRetries || retryAfter > maxRetryAfter:
			return fmt.Errorf("%s, %d tries: %w", op, attempt+1, err)
		}

		if retryAfter <= 0 {
			retryAfter = firstBackoff << attempt
		}

		if err := c.wait(ctx, retryAfter); err != nil {
			return fmt.Errorf("wait %s before retrying %s: %w", retryAfter, op, err)
		}
	}
}

// response is the status, headers and body of one answer.
type response struct {
	status     int
	retryAfter string
	body       []byte
}

// send makes one request with the bot's token and reads the answer.
func (c *client) send(ctx context.Context, method, target string, data []byte) (response, string, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return response{}, "", fmt.Errorf("get a Bot Framework token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(data))
	if err != nil {
		return response{}, "", fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return response{}, "", fmt.Errorf("send the request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(httpclient.LimitBody(resp.Body, maxResponseBytes, errTooLarge))
	if err != nil {
		return response{}, "", fmt.Errorf("read the response: %w", err)
	}

	return response{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After"), body: body}, token, nil
}

// answer reads one answer into out. It returns errRetryLater, with the
// wait Teams asked for, for an answer to retry, and errNotFound for 404.
func (c *client) answer(op string, resp response, out any) (time.Duration, error) {
	switch resp.status {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
	case http.StatusTooManyRequests, http.StatusPreconditionFailed, http.StatusBadGateway, http.StatusGatewayTimeout:
		c.metrics.apiError(op, strconv.Itoa(resp.status))

		return parseRetryAfter(resp.retryAfter), fmt.Errorf("HTTP %d: %w", resp.status, errRetryLater)
	case http.StatusNotFound:
		c.metrics.apiError(op, strconv.Itoa(resp.status))

		return 0, errNotFound
	default:
		c.metrics.apiError(op, strconv.Itoa(resp.status))

		return 0, fmt.Errorf("teams answered HTTP %d", resp.status)
	}

	if out == nil || len(bytes.TrimSpace(resp.body)) == 0 {
		return 0, nil
	}

	if err := json.Unmarshal(resp.body, out); err != nil {
		c.metrics.apiError(op, statusBadResponse)

		return 0, fmt.Errorf("decode the response: %w", err)
	}

	return 0, nil
}

// checkServiceURL reports whether the bot's token may be sent to raw: an
// https URL on a Bot Framework host or the configured service URL's host,
// or a loopback URL.
func (c *client) checkServiceURL(raw string) error {
	if err := bot.CheckBaseURL(ensureSlash(raw)); err != nil {
		return fmt.Errorf("service URL: %w", err)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse the service URL: %w", err)
	}

	host := strings.ToLower(u.Hostname())
	if host == c.allowedHost || httpclient.IsLoopbackHost(host) || slices.Contains(serviceHosts, host) ||
		slices.ContainsFunc(serviceHostSuffixes, func(s string) bool { return strings.HasSuffix(host, s) }) {
		return nil
	}

	return fmt.Errorf("service URL host %q is not a Bot Framework host", host)
}

// limiter returns the limiter of key, making it on first use.
func (c *client) limiter(key string) *rate.Limiter {
	c.mu.Lock()
	defer c.mu.Unlock()

	l, ok := c.limiters[key]
	if !ok {
		l = rate.NewLimiter(c.limit, conversationBurst)
		c.limiters[key] = l
	}

	return l
}

// conversationOf returns the conversation ID of the thread whose first
// message is thread, in channel.
func conversationOf(channel, thread string) string {
	return channel + ";messageid=" + thread
}

// ensureSlash returns s ending in "/".
func ensureSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}

	return s + "/"
}

// parseRetryAfter reads a Retry-After header in whole seconds. A missing
// or unusable value gives 0, which means backing off.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return 0
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
