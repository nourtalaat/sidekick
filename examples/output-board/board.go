// Package board is an example output sink for a made-up status board. The
// board has a small JSON API: POST /cards creates a card and returns its
// ID, and PUT /cards/{id} replaces it. The sink keeps one card per
// investigation and edits it as the investigation runs. It is built and
// tested in CI, but nothing imports it, so it is not part of the sidekick
// binary. docs/plugins/outputs.md walks through it; the step numbers in
// the comments below match the guide.
package board

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Type is the output type this package registers. A config selects it
// with "type: board".
const Type = "board"

// maxTextChars is the longest text the board shows on a card.
const maxTextChars = 500

// maxAnswerBytes is the most of an answer the sink reads.
const maxAnswerBytes = 1 << 20

// dataSeq is the key of Ref.Data that holds the Seq the card shows.
const dataSeq = "seq"

var (
	errBadURL     = errors.New("url: expected an absolute http or https URL")
	errNoToken    = errors.New("tokenSecret: required")
	errNoSecrets  = errors.New("no secrets resolver to read the token with")
	errHTTPStatus = errors.New("the board answered with an error status")
)

// init registers the sink when the package is linked in (step 1). A
// built-in sink also gets a blank import in internal/plugins (step 6).
func init() {
	registry.Outputs.Register(Type, registry.Typed(New))
}

// Config is the section of one board output (step 1).
type Config struct {
	// URL is the board API's base URL.
	URL string `yaml:"url"`
	// TokenSecret holds the API token. It is a reference to a mounted
	// secret, never the token itself.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
}

// Validate checks the section without network calls or secrets (step 1).
// Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, errBadURL)
	}

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errNoToken)
	}

	return errors.Join(errs...)
}

// Sink keeps one card per investigation on the board.
type Sink struct {
	name    string
	baseURL string
	token   registry.SecretRef
	secrets registry.Secrets
	http    *http.Client
}

var _ output.Sink = (*Sink)(nil)

// card is what the board API takes and returns.
type card struct {
	ID     string `json:"id,omitempty"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

// Accepts reports which phases the board shows (step 2): the running
// investigation and its result. It skips the queued phase, so a card
// appears once the investigation has started.
func (s *Sink) Accepts(phase output.Phase) bool {
	return phase == output.PhaseRunning || phase == output.PhaseFinal
}

// Publish creates the card, or replaces it when u.Previous is set (step
// 3). An update whose Seq is not newer than the one the card shows is
// dropped, and the returned ref records the Seq the card now shows.
func (s *Sink) Publish(ctx context.Context, u output.Update) (output.Ref, error) {
	shown := int64(-1)
	if u.Previous != nil {
		if seq, err := strconv.ParseInt(u.Previous.Data[dataSeq], 10, 64); err == nil {
			shown = seq
		}

		if u.Seq <= shown {
			return *u.Previous, nil
		}
	}

	c := render(u)

	method, path := http.MethodPost, "/cards"
	if u.Previous != nil {
		method, path = http.MethodPut, "/cards/"+url.PathEscape(u.Previous.External)
	}

	saved, err := s.send(ctx, method, path, c)
	if err != nil {
		return output.Ref{}, fmt.Errorf("save the card: %w", err)
	}

	return output.Ref{Sink: s.name, External: saved.ID, Data: map[string]string{dataSeq: strconv.FormatInt(u.Seq, 10)}}, nil
}

// send makes one call to the board API (step 5). The token is read on
// every call, so a rotated token is used without a restart. Any error is
// returned, and the publish job retries it with backoff.
func (s *Sink) send(ctx context.Context, method, path string, c card) (card, error) {
	token, err := secrets.ResolveText(ctx, s.secrets, s.token)
	if err != nil {
		return card{}, fmt.Errorf("read tokenSecret: %w", err)
	}

	body, err := json.Marshal(c)
	if err != nil {
		return card{}, fmt.Errorf("encode the card: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return card{}, fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return card{}, fmt.Errorf("send the request: %w", err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return card{}, fmt.Errorf("%w: %d", errHTTPStatus, resp.StatusCode)
	}

	var saved card
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswerBytes)).Decode(&saved); err != nil {
		return card{}, fmt.Errorf("decode the answer: %w", err)
	}

	return saved, nil
}

// New builds a sink from its validated config (step 1). It sends requests
// with deps.HTTP, or a client from httpclient.New when that is nil. It
// must not call the board here: a board that is down must not stop
// Sidekick from starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (output.Sink, error) {
	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	return &Sink{
		name:    name,
		baseURL: strings.TrimSuffix(cfg.URL, "/"),
		token:   cfg.TokenSecret,
		secrets: deps.Secrets,
		http:    hc,
	}, nil
}

// render turns an update into a card (step 4). The report's text comes
// from the model and the title from the alert sender, so both are
// untrusted: the board shows plain text, so they are only cut to length
// here, but a sink whose target reads markup must escape them, as the
// Slack output does.
func render(u output.Update) card {
	c := card{Title: cut(u.Event.Title), Status: "investigating", Text: "Investigating"}

	if n := len(u.Progress); n > 0 {
		c.Text = cut(u.Progress[n-1].Text)
	}

	if u.Phase == output.PhaseFinal && u.Report != nil && u.Report.StopReason != report.StopError {
		c.Status = string(u.Report.Verdict)
		c.Text = cut(u.Report.Reason)
	}

	if u.Phase == output.PhaseFinal && (u.Report == nil || u.Report.StopReason == report.StopError) {
		c.Status, c.Text = "failed", "Could not finish"
	}

	return c
}

// cut returns s with at most maxTextChars characters.
func cut(s string) string {
	runes := []rune(s)
	if len(runes) <= maxTextChars {
		return s
	}

	return string(runes[:maxTextChars])
}
