// Package echo is an example model connector. It answers every turn with
// the text of the last user message, so it needs no network and no secrets.
// It is built and tested in CI, but nothing imports it, so it is not part
// of the sidekick binary. docs/plugins/model-connectors.md walks through it;
// the step numbers in the comments below match the guide.
package echo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Provider is the provider name this connector registers. A config selects
// it with "provider: echo".
const Provider = "echo"

// maxOutputTokens is the most output the echo model returns, in tokens.
const maxOutputTokens = 4096

// errNoUserText is returned when the conversation has no user text to echo.
var errNoUserText = errors.New("no user text to echo")

// errPrefixSpaces is returned for a prefix with spaces at either end.
var errPrefixSpaces = errors.New("prefix: must not start or end with spaces")

// init registers the provider when the package is linked in (step 7). A
// built-in connector also gets a blank import in internal/plugins.
func init() {
	registry.Models.Register(Provider, registry.Typed(New))
}

// Config is the config section of one instance (step 1). It embeds the
// shared settings inline, so the connector's own keys sit next to model,
// maxTokens and the rest.
type Config struct {
	model.Settings `yaml:",inline"`

	// Prefix is put before every answer.
	Prefix string `yaml:"prefix"`
}

var _ model.CapabilityDeclarer = (*Config)(nil)

// Capabilities says what the model can do (step 2). Declaring it on the
// config lets sidekick config validate check the settings and roles
// without building the connector.
func (c *Config) Capabilities() model.Capabilities {
	return model.Capabilities{MaxOutputTokens: maxOutputTokens}
}

// Validate checks the section (step 2). Each error starts with the key of
// its field, so config validation can point at it.
func (c *Config) Validate() error {
	var errs []error

	if err := c.Settings.Validate(c.Capabilities()); err != nil {
		errs = append(errs, err)
	}

	if strings.TrimSpace(c.Prefix) != c.Prefix {
		errs = append(errs, errPrefixSpaces)
	}

	return errors.Join(errs...)
}

// Connector echoes the last user message.
type Connector struct {
	cfg Config
}

var _ model.Connector = (*Connector)(nil)

// Capabilities reports what the configured model supports.
func (c *Connector) Capabilities() model.Capabilities {
	return c.cfg.Capabilities()
}

// ModelID names the configured model for telemetry labels and prices
// (step 6).
func (c *Connector) ModelID() string {
	return c.cfg.Model
}

// Generate runs one model turn (step 4). It walks the sealed part types
// with a type switch and reports usage, never cost.
func (c *Connector) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrTimeout, Err: fmt.Errorf("echo: %w", err)}
	}

	start := time.Now()

	text, err := lastUserText(req.Messages)
	if err != nil {
		// Every failure is a *model.Error (step 5), so the retry helper
		// knows whether to try again.
		return model.Response{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("echo: %w", err)}
	}

	answer := c.cfg.Prefix + text

	return model.Response{
		Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: answer}}},
		StopReason: model.StopEndTurn,
		Usage:      model.Usage{InputTokens: int64(len(strings.Fields(text))), OutputTokens: int64(len(strings.Fields(answer)))},
		Model:      c.cfg.Model,
		Latency:    time.Since(start),
	}, nil
}

// New builds an instance from its validated config (step 3). A real
// connector reads its API key through deps.Secrets here and sends requests
// with deps.HTTP.
func New(_ context.Context, _ string, cfg Config, _ registry.Deps) (model.Connector, error) {
	return &Connector{cfg: cfg}, nil
}

// lastUserText returns the text of the last user message that has text.
// Tool calls, tool results and reasoning carry nothing to echo.
func lastUserText(msgs []model.Message) (string, error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != model.User {
			continue
		}

		var b strings.Builder

		for _, part := range msgs[i].Parts {
			switch p := part.(type) {
			case model.Text:
				b.WriteString(p.Text)
			case model.ToolCall, model.ToolResult, model.Reasoning:
			}
		}

		if b.Len() > 0 {
			return b.String(), nil
		}
	}

	return "", errNoUserText
}
