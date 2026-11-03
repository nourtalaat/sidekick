package alertmanagersilence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/httpauth"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The limits on what the client reads from Alertmanager.
const (
	// maxResponseBytes is the largest answer read. A list of silences
	// holds every silence not yet removed, expired ones included.
	maxResponseBytes = 8 << 20
	// maxErrorBytes is how much of an error answer is read for its message.
	maxErrorBytes = 64 << 10
	// maxMessageBytes is the longest message of Alertmanager that is kept.
	maxMessageBytes = 400
)

// stateExpired is the state of a silence that has ended.
const stateExpired = "expired"

var (
	errTooLarge = errors.New("the answer from Alertmanager is larger than the action reads")
	errNoBody   = errors.New("the answer from Alertmanager is not something this action can read")
)

// statusError is an answer of Alertmanager that is not a success.
type statusError struct {
	code int
	// message is Alertmanager's own message, untrusted, cut.
	message string
}

// Error implements error.
func (e *statusError) Error() string {
	text := fmt.Sprintf("Alertmanager answered %d", e.code)
	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// client sends requests to the Alertmanager API with the action's
// credentials.
type client struct {
	http      *http.Client
	root      string
	userAgent string
	auth      *httpauth.Auth
}

// silence is a silence as Alertmanager lists it, with the state it has now.
type silence struct {
	ID        string
	State     string
	Matchers  []mute.Matcher
	StartsAt  time.Time
	EndsAt    time.Time
	CreatedBy string
	Comment   string
}

// wireSilence is a silence as the JSON of GET /api/v2/silences writes it.
type wireSilence struct {
	ID     string `json:"id"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
	Matchers []struct {
		Name    string `json:"name"`
		Value   string `json:"value"`
		IsRegex bool   `json:"isRegex"`
		// IsEqual is absent in answers of Alertmanagers that predate it,
		// and then means equal.
		IsEqual *bool `json:"isEqual"`
	} `json:"matchers"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
}

// alert is an alert as Alertmanager lists it, with what the preview shows.
type alert struct {
	Labels map[string]string
	State  string
}

// postedSilence is the body of the call that makes a silence.
type postedSilence struct {
	Matchers  []mute.Matcher `json:"matchers"`
	StartsAt  time.Time      `json:"startsAt"`
	EndsAt    time.Time      `json:"endsAt"`
	CreatedBy string         `json:"createdBy"`
	Comment   string         `json:"comment"`
}

// silences lists every silence, in every state: GET /api/v2/silences.
func (c *client) silences(ctx context.Context) ([]silence, error) {
	var raw []wireSilence

	if err := c.do(ctx, http.MethodGet, "/api/v2/silences", nil, nil, &raw); err != nil {
		return nil, fmt.Errorf("list the silences: %w", err)
	}

	out := make([]silence, 0, len(raw))

	for _, r := range raw {
		s := silence{ID: r.ID, State: r.Status.State, StartsAt: r.StartsAt, EndsAt: r.EndsAt, CreatedBy: r.CreatedBy, Comment: r.Comment}

		for _, m := range r.Matchers {
			s.Matchers = append(s.Matchers, mute.Matcher{Name: m.Name, Value: m.Value, IsRegex: m.IsRegex, IsEqual: m.IsEqual == nil || *m.IsEqual})
		}

		out = append(out, s)
	}

	return out, nil
}

// alerts lists the firing alerts that match all the matchers:
// GET /api/v2/alerts with one filter for each matcher.
func (c *client) alerts(ctx context.Context, matchers []mute.Matcher) ([]alert, error) {
	query := url.Values{}
	for _, m := range matchers {
		query.Add("filter", m.Expression())
	}

	var raw []struct {
		Labels map[string]string `json:"labels"`
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}

	if err := c.do(ctx, http.MethodGet, "/api/v2/alerts", query, nil, &raw); err != nil {
		return nil, fmt.Errorf("list the alerts: %w", err)
	}

	out := make([]alert, 0, len(raw))
	for _, r := range raw {
		out = append(out, alert{Labels: r.Labels, State: r.Status.State})
	}

	return out, nil
}

// create makes a silence and returns its ID: POST /api/v2/silences, which
// answers {"silenceID": "..."}. The body has no id, so it is always a new
// silence.
func (c *client) create(ctx context.Context, body postedSilence) (string, error) {
	var out struct {
		SilenceID string `json:"silenceID"`
	}

	if err := c.do(ctx, http.MethodPost, "/api/v2/silences", nil, body, &out); err != nil {
		return "", fmt.Errorf("create the silence: %w", err)
	}

	if out.SilenceID == "" {
		return "", fmt.Errorf("create the silence: %w: no silenceID", errNoBody)
	}

	return out.SilenceID, nil
}

// expire ends the silence with id now: DELETE /api/v2/silence/{id}, which
// expires it and does not delete it. A silence that is gone already is not
// an error.
func (c *client) expire(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/api/v2/silence/"+url.PathEscape(id), nil, nil, nil)

	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return nil
	}

	if err != nil {
		return fmt.Errorf("expire silence %s: %w", id, err)
	}

	return nil
}

// do sends one request and decodes the JSON answer into out, if out is set.
// An answer that is not a success returns a *statusError, wrapped with
// action.ErrPermanent when sending it again cannot help: 400, 401, 403,
// 404, 409 and 422. A 429 or a server error is not, so Sidekick tries
// again.
func (c *client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode the body: %w", err)
		}

		reader = bytes.NewReader(raw)
	}

	target := c.root + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	if err := c.auth.Apply(ctx, req); err != nil {
		return fmt.Errorf("add the credentials: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send the request: %w", err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	limited := httpclient.LimitBody(resp.Body, maxResponseBytes, errTooLarge)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return statusFailure(resp.StatusCode, limited)
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("decode the answer: %w: %w", errNoBody, err)
	}

	return nil
}

// statusFailure makes the error of an answer that is not a success.
func statusFailure(code int, body io.Reader) error {
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBytes))
	if err == nil {
		e.message = source.CutText(messageOf(data), maxMessageBytes)
	}

	switch code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %w", action.ErrPermanent, e)
	default:
		return e
	}
}

// messageOf reads the message of an error answer. Alertmanager sends a JSON
// string or {"message": "..."}; a proxy in front of it may send plain text.
func messageOf(data []byte) string {
	var text string
	if json.Unmarshal(data, &text) == nil {
		return strings.TrimSpace(text)
	}

	var parsed struct {
		Message string `json:"message"`
	}

	if json.Unmarshal(data, &parsed) == nil {
		return strings.TrimSpace(parsed.Message)
	}

	return strings.TrimSpace(string(data))
}
