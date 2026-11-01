// Package note is an example action for a made-up notes service. The
// service has a small JSON API: POST /boards/{board}/notes creates a note
// and returns its ID and address. The action writes one note to a board
// that the config allows, when an approver approves the model's proposal.
// It is built and tested in CI, but nothing imports it, so it is not part
// of the sidekick binary. docs/plugins/actions.md walks through it; the
// step numbers in the comments below match the guide.
package note

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
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Type is the action type this package registers. A config selects it
// with "type: note.create".
const Type = "note.create"

// maxAnswerBytes is the most of an answer the action reads.
const maxAnswerBytes = 1 << 20

// defaultMaxBodyChars is the longest note body when the limits set none.
const defaultMaxBodyChars = 2000

// idempotencyHeader is the header the notes service reads to find the note
// a repeated request made already.
const idempotencyHeader = "Idempotency-Key"

var (
	errBadURL      = errors.New("url: expected an absolute https URL, or http on a loopback host")
	errNoToken     = errors.New("tokenSecret: required")
	errNoBoards    = errors.New("limits.boards: at least one board is required")
	errNegative    = errors.New("limits.maxBodyChars: must not be negative")
	errNoSecrets   = errors.New("no secrets resolver to read the token with")
	errTooLarge    = errors.New("the notes service answered with more than the action reads")
	errServer      = errors.New("the notes service failed")
	errRefused     = errors.New("the notes service refused the note")
	errBoardLimit  = errors.New("board is not one of limits.boards")
	errBodyLimit   = errors.New("body is longer than limits.maxBodyChars")
	errBadResponse = errors.New("the notes service answered with something this action cannot read")
)

// init registers the action when the package is linked in (step 1). A
// built-in action also gets a blank import in internal/plugins (step 7).
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Config is the section of one note.create action (step 1).
type Config struct {
	// URL is the notes service's base URL.
	URL string `yaml:"url"`
	// TokenSecret holds the service's API token. It is a reference to a
	// mounted secret, never the token itself. Give the action a token of
	// its own, that can only write notes, apart from any token a source uses.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Limits say what the action may touch. They are checked in Validate,
	// when the model proposes and again before the action runs.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a note.create action (step 1).
type Limits struct {
	// Boards are the only boards the action may write to.
	Boards []string `yaml:"boards"`
	// MaxBodyChars is the longest note body, in characters. Zero means the
	// default.
	MaxBodyChars int `yaml:"maxBodyChars"`
}

// Validate checks the section without network calls or secrets (step 1).
// Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if !credentialSafe(c.URL) {
		errs = append(errs, errBadURL)
	}

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errNoToken)
	}

	if len(c.Limits.Boards) == 0 {
		errs = append(errs, errNoBoards)
	}

	if c.Limits.MaxBodyChars < 0 {
		errs = append(errs, errNegative)
	}

	return errors.Join(errs...)
}

// credentialSafe reports whether raw is an absolute https URL, or an http
// URL on a loopback host: a token must not travel in the clear.
func credentialSafe(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}

	return u.Scheme == "https" || (u.Scheme == "http" && httpclient.IsLoopbackHost(u.Hostname()))
}

// Params are what the model fills in when it proposes the action (step 2).
type Params struct {
	Board string `json:"board"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// paramsSchema is the JSON Schema of Params (step 2). Sidekick checks the
// parameters against it before it calls Validate, and the model reads it.
var paramsSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["board", "title"],
  "properties": {
    "board": {"type": "string", "description": "The board to write the note on."},
    "title": {"type": "string", "minLength": 1, "maxLength": 120},
    "body": {"type": "string"}
  }
}`)

// Note is one configured note.create action.
type Note struct {
	name    string
	baseURL string
	token   registry.SecretRef
	limits  Limits
	secrets registry.Secrets
	http    *http.Client
}

var _ action.Action = (*Note)(nil)

// created is what the notes service returns for a note.
type created struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Spec implements action.Action (step 2). The description tells the model
// when to use the action and what it may touch; the schema tells it the
// parameters.
func (n *Note) Spec() action.Spec {
	return action.Spec{
		Name:         n.name,
		Description:  "Writes a note on a board of the notes service. Allowed boards: " + strings.Join(n.limits.Boards, ", ") + ".",
		ParamsSchema: paramsSchema,
	}
}

// Validate implements action.Action (step 3). It runs when the model
// proposes the action, and again before the action runs, with the limits
// as the config has them by then. A limit violation wraps action.ErrLimit,
// so Sidekick fails the proposal instead of trying again.
func (n *Note) Validate(_ context.Context, raw json.RawMessage) error {
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("decode the parameters: %w", err)
	}

	if !slices.Contains(n.limits.Boards, p.Board) {
		return fmt.Errorf("board %q: %w: %w", p.Board, action.ErrLimit, errBoardLimit)
	}

	if len([]rune(p.Body)) > n.maxBody() {
		return fmt.Errorf("%d characters: %w: %w", len([]rune(p.Body)), action.ErrLimit, errBodyLimit)
	}

	return nil
}

// Execute implements action.Action (step 4). It checks the parameters
// again, then asks the service for the note with the idempotency key of the
// proposal. Sidekick may call Execute again with the same key after a
// crash, and the service then returns the note it made the first time, so
// the note exists once.
func (n *Note) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	if err := n.Validate(ctx, inv.Params); err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	var p Params
	if err := json.Unmarshal(inv.Params, &p); err != nil {
		return action.Result{}, fmt.Errorf("decode the parameters: %w", err)
	}

	made, err := n.create(ctx, p, inv.IdempotencyKey)
	if err != nil {
		return action.Result{}, fmt.Errorf("create the note: %w", err)
	}

	return action.Result{
		Summary: fmt.Sprintf("Wrote the note %q on board %s.", p.Title, p.Board),
		Links:   []action.Link{{Title: "Note " + made.ID, URL: made.URL}},
	}, nil
}

// create makes one call to the notes service (step 5). The token is read on
// every call, so a rotated token is used without a restart.
func (n *Note) create(ctx context.Context, p Params, key string) (created, error) {
	token, err := secrets.ResolveText(ctx, n.secrets, n.token)
	if err != nil {
		return created{}, fmt.Errorf("read tokenSecret: %w", err)
	}

	body, err := json.Marshal(map[string]string{"title": p.Title, "body": p.Body})
	if err != nil {
		return created{}, fmt.Errorf("encode the note: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.baseURL+"/boards/"+url.PathEscape(p.Board)+"/notes", bytes.NewReader(body))
	if err != nil {
		return created{}, fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(idempotencyHeader, key)

	resp, err := n.http.Do(req)
	if err != nil {
		return created{}, fmt.Errorf("send the request: %w", err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden:
		// A repeat cannot fix these, so the proposal fails at once.
		return created{}, fmt.Errorf("status %d: %w: %w", resp.StatusCode, action.ErrPermanent, errRefused)
	default:
		// A server error or a rate limit may pass: Sidekick tries again.
		return created{}, fmt.Errorf("status %d: %w", resp.StatusCode, errServer)
	}

	var made created

	limited := httpclient.LimitBody(resp.Body, maxAnswerBytes, errTooLarge)
	if err := json.NewDecoder(limited).Decode(&made); err != nil && !errors.Is(err, io.EOF) {
		return created{}, fmt.Errorf("decode the answer: %w: %w", errBadResponse, err)
	}

	if made.ID == "" {
		return created{}, fmt.Errorf("the answer has no note ID: %w", errBadResponse)
	}

	return made, nil
}

// maxBody returns the longest body the limits allow.
func (n *Note) maxBody() int {
	if n.limits.MaxBodyChars > 0 {
		return n.limits.MaxBodyChars
	}

	return defaultMaxBodyChars
}

// New builds the action from its validated config (step 1). It must not
// call the service here: a service that is down must not stop Sidekick from
// starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	return &Note{
		name:    name,
		baseURL: strings.TrimSuffix(cfg.URL, "/"),
		token:   cfg.TokenSecret,
		limits:  cfg.Limits,
		secrets: deps.Secrets,
		http:    hc,
	}, nil
}
