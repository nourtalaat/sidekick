// Package uptime is an example webhook intake adapter for a made-up uptime
// checker. The checker posts one JSON object each time a check goes down
// or comes back up, and signs the body in its own way. The adapter turns
// each delivery into one event. It is built and tested in CI, but nothing
// imports it, so it is not part of the sidekick binary.
// docs/plugins/intake-adapters.md walks through it; the step numbers in the
// comments below match the guide.
package uptime

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the adapter name this package registers. A webhook endpoint
// selects it with "adapter: uptime".
const Type = "uptime"

// SignatureHeader holds the checker's signature: the hex HMAC-SHA256 of
// the body, keyed with the endpoint's secret.
const SignatureHeader = "X-Uptime-Signature"

// The states the checker sends.
const (
	stateDown = "down"
	stateUp   = "up"
)

var (
	errServiceSpaces = errors.New("service: must not start or end with spaces")
	errNoCheckID     = errors.New("check_id: required")
	errBadState      = errors.New(`state: must be "down" or "up"`)
	errNoSince       = errors.New("since: required")
	errNoSignature   = errors.New("missing signature")
	errBadSignature  = errors.New("wrong signature")
)

// init registers the adapter when the package is linked in (step 6). A
// built-in adapter also gets a blank import in internal/plugins.
func init() {
	registry.Intakes.Register(Type, registry.Typed(New))
}

// Config is the adapter's part of a webhook endpoint's config (step 1).
// The endpoint's adapter, auth and trusted keys belong to Sidekick; the
// other keys of the endpoint are decoded into this struct.
type Config struct {
	// Service is the service of checks whose payload names none.
	Service string `yaml:"service"`
}

// Validate checks the section (step 1). Each error starts with the key of
// its field, so config validation can point at it.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Service) != c.Service {
		return errServiceSpaces
	}

	return nil
}

// Checker turns the uptime checker's deliveries into events.
type Checker struct {
	cfg Config
}

var (
	_ intake.Adapter           = (*Checker)(nil)
	_ intake.SignatureVerifier = (*Checker)(nil)
)

// New builds an adapter from its validated config (step 1). An adapter
// that needs a secret of its own reads it through deps.Secrets here.
func New(_ context.Context, _ string, cfg Config, _ registry.Deps) (intake.Adapter, error) {
	return &Checker{cfg: cfg}, nil
}

// payload is one delivery of the checker (step 2).
type payload struct {
	CheckID string    `json:"check_id"`
	Name    string    `json:"name"`
	URL     string    `json:"url"`
	State   string    `json:"state"`
	Since   time.Time `json:"since"`
	Message string    `json:"message"`
	Service string    `json:"service"`
}

// Parse implements intake.Adapter (steps 2 to 4). It returns one event.
// Errors name the field that is wrong, never a value from the payload,
// because they are logged and sent back to the sender.
func (c *Checker) Parse(_ context.Context, req intake.Request) ([]event.Event, error) {
	var p payload
	if err := json.Unmarshal(req.Body, &p); err != nil {
		return nil, fmt.Errorf("decode the uptime payload: %w", err)
	}

	if p.CheckID == "" {
		return nil, errNoCheckID
	}

	if p.Since.IsZero() {
		return nil, errNoSince
	}

	// Step 4: a check that comes back up is a resolved event.
	kind := event.KindAlert

	switch p.State {
	case stateDown:
	case stateUp:
		kind = event.KindResolved
	default:
		return nil, errBadState
	}

	service := cmp.Or(p.Service, c.cfg.Service)
	labels := map[string]string{"check": p.CheckID}

	ev := event.Event{
		// Step 3: the same outage always gives the same key, so a
		// delivery the checker sends twice is stored once.
		Key:         p.CheckID + ":" + p.State + ":" + p.Since.UTC().Format(time.RFC3339),
		Kind:        kind,
		Sender:      event.Sender{Kind: event.SenderSystem, Name: Type},
		AlertName:   p.Name,
		Title:       cmp.Or(p.Name, p.CheckID) + " is " + p.State,
		Text:        p.Message,
		Service:     service,
		Severity:    event.SeverityCritical,
		Labels:      labels,
		Fingerprint: event.Fingerprint(Type, p.CheckID, labels, []string{"check"}),
		StartsAt:    p.Since,
		ReceivedAt:  req.ReceivedAt,
		Raw:         json.RawMessage(req.Body),
	}

	if p.URL != "" {
		ev.Links = []event.Link{{Title: "Checked URL", URL: p.URL}}
	}

	return []event.Event{ev}, nil
}

// VerifySignature implements intake.SignatureVerifier (step 5). Sidekick
// calls it instead of its own bearer or HMAC check, with the secret the
// endpoint's auth names, before Parse sees the body.
func (c *Checker) VerifySignature(req intake.Request, secret []byte) error {
	sig := req.Header.Get(SignatureHeader)
	if sig == "" {
		return fmt.Errorf("%w: %w", intake.ErrUnauthenticated, errNoSignature)
	}

	got, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("%w: decode signature: %w", intake.ErrUnauthenticated, err)
	}

	if !hmac.Equal(got, Sign(secret, req.Body)) {
		return fmt.Errorf("%w: %w", intake.ErrUnauthenticated, errBadSignature)
	}

	return nil
}

// Sign returns the signature the checker sends for body.
func Sign(secret, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)

	return mac.Sum(nil)
}
