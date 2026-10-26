// Package hello is an example source. Its one read-only tool greets a
// person by name, so it needs no network and no secrets. It is built and
// tested in CI, but nothing imports it, so it is not part of the sidekick
// binary. docs/plugins/sources.md walks through it; the step numbers in
// the comments below match the guide.
package hello

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers. A config selects it with
// "type: hello".
const Type = "hello"

// Tool names, as the source names them. The model sees them with the
// source prefix, for example hello__greet.
const (
	toolGreet       = "greet"
	toolSetGreeting = "set_greeting"
)

var (
	// errGreetingSpaces is returned for a greeting with spaces at either end.
	errGreetingSpaces = errors.New("greeting: must not start or end with spaces")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

// greetSchema is the JSON Schema of the greet tool's input. The guard
// checks the model's input against it before Call sees it.
var greetSchema = json.RawMessage(`{
	"type": "object",
	"properties": {"name": {"type": "string", "description": "Who to greet."}},
	"required": ["name"],
	"additionalProperties": false
}`)

// init registers the source type when the package is linked in (step 1). A
// built-in source also gets a blank import in internal/plugins (step 5).
func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Config is the config section of one instance (step 1). It embeds the
// shared source settings inline, so tools, timeout and concurrency sit
// next to the source's own keys and are checked the same way for every
// source.
type Config struct {
	source.Settings `yaml:",inline"`

	// Greeting starts every answer. Empty means "Hello".
	Greeting string `yaml:"greeting"`
}

// Validate checks the section without network calls (step 1). Each error
// starts with the key of its field, so config validation can point at it.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Greeting) != c.Greeting {
		return errGreetingSpaces
	}

	return nil
}

// Source greets people.
type Source struct {
	greeting string
}

var _ source.Source = (*Source)(nil)

// Tools lists every tool the source has, before filtering (step 2). The
// set_greeting tool would change the source, so its ReadOnly flag is false
// and the guard never shows it to the model, whatever tools.allow says.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return []source.Tool{
		{Name: toolGreet, Description: "Greet a person by name.", InputSchema: greetSchema, ReadOnly: true},
		{Name: toolSetGreeting, Description: "Change the greeting.", InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: false},
	}, nil
}

// Call runs one tool (step 2). An empty answer is NoData, not an error; a
// link a person can open to check the answer goes in Evidence.
func (s *Source) Call(_ context.Context, call source.Call) (source.Result, error) {
	if call.Tool != toolGreet {
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}

	var in struct {
		Name string `json:"name"`
	}

	if err := json.Unmarshal(call.Input, &in); err != nil {
		return source.Result{}, fmt.Errorf("decode input: %w", err)
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return source.Result{NoData: true}, nil
	}

	return source.Result{
		Text:     s.greeting + ", " + name + "!",
		Evidence: []source.Evidence{{Kind: source.EvidenceOther, Title: "Greetings guide", URL: "https://example.com/greetings"}},
	}, nil
}

// Close releases nothing; a real source closes its connections here.
func (s *Source) Close() error {
	return nil
}

// New builds an instance from its validated config (step 1). A real source
// reads its credentials through deps.Secrets and sends requests with
// deps.HTTP here. It must not wait for a remote service: connect in the
// background instead, so a service that is down does not stop Sidekick.
func New(_ context.Context, _ string, cfg Config, _ registry.Deps) (source.Source, error) {
	greeting := cfg.Greeting
	if greeting == "" {
		greeting = "Hello"
	}

	return &Source{greeting: greeting}, nil
}
