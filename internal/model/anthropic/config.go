package anthropic

import (
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// minBudgetTokens is the smallest thinking budget the API accepts.
const minBudgetTokens = 1024

// defaultMaxTokens is the output limit of a call when neither the request
// nor the config sets one. The connector streams, so a large limit does
// not risk an HTTP timeout; the model's own maximum still applies.
const defaultMaxTokens = 64_000

// errTemperature explains why temperature is rejected.
var errTemperature = errors.New("temperature: not accepted; the Anthropic API version this connector uses (2026-10-21) rejects temperature, top_p and top_k on every model")

// Config is the config section of one Anthropic connector instance.
type Config struct {
	model.Settings `yaml:",inline"`

	// APIKeySecret holds the Anthropic API key.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// BaseURL replaces the API address, for example for a proxy.
	BaseURL string `yaml:"baseURL"`
	// Declared describes a model the connector does not know. It replaces
	// the known capabilities when set.
	Declared *DeclaredCapabilities `yaml:"capabilities"`
}

var _ model.CapabilityDeclarer = (*Config)(nil)

// DeclaredCapabilities is the capabilities block of a model the connector
// does not know, in config.
type DeclaredCapabilities struct {
	ToolCalling      bool                 `yaml:"toolCalling"`
	ToolChoice       bool                 `yaml:"toolChoice"`
	ThinkingModes    []model.ThinkingMode `yaml:"thinkingModes"`
	EffortLevels     []model.Effort       `yaml:"effortLevels"`
	StructuredOutput bool                 `yaml:"structuredOutput"`
	PromptCaching    bool                 `yaml:"promptCaching"`
	ContextTokens    int                  `yaml:"contextTokens"`
	MaxOutputTokens  int                  `yaml:"maxOutputTokens"`
}

// Capabilities implements model.CapabilityDeclarer: the declared block when
// there is one, otherwise the table entry of the model, otherwise nothing.
func (c *Config) Capabilities() model.Capabilities {
	m, _ := c.known()

	return m.caps
}

// Validate checks the section without secrets or network calls: the model
// must be known or declared, the shared settings must fit its
// capabilities, and the Anthropic rules for thinking and temperature must
// hold. Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.Temperature != nil {
		errs = append(errs, errTemperature)
	}

	if c.BaseURL != "" {
		if err := checkBaseURL(c.BaseURL); err != nil {
			errs = append(errs, err)
		}
	}

	m, ok := c.known()

	switch {
	case !ok && c.Model == "":
		errs = append(errs, errors.New("model: required"))

		return errors.Join(errs...)
	case !ok:
		errs = append(errs, fmt.Errorf("model: %q is not a model the anthropic provider knows; describe it in a capabilities block", c.Model))

		return errors.Join(errs...)
	}

	if c.Declared != nil {
		errs = append(errs, c.Declared.check()...)
	}

	if err := c.Settings.Validate(m.caps); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, checkThinking(c.Model, m, c.Thinking, c.Effort, c.maxTokens(0, m.caps))...)

	return errors.Join(errs...)
}

// known returns what the connector knows about the configured model, and
// false when it knows nothing: the model is neither declared nor in the
// table.
func (c *Config) known() (knownModel, bool) {
	if c.Declared != nil {
		return knownModel{caps: c.Declared.capabilities(), off: offDisable}, true
	}

	m, ok := knownModels[c.Model]

	return m, ok
}

// maxTokens returns the output limit of a call: the request's, else the
// config's, else defaultMaxTokens, capped at the model's maximum.
func (c *Config) maxTokens(requested int, caps model.Capabilities) int {
	n := cmpOr(requested, c.MaxTokens, defaultMaxTokens)
	if caps.MaxOutputTokens > 0 && n > caps.MaxOutputTokens {
		return caps.MaxOutputTokens
	}

	return n
}

// check returns problems with a capabilities block.
func (d *DeclaredCapabilities) check() []error {
	var errs []error

	for _, m := range d.ThinkingModes {
		if m != model.ThinkingAdaptive && m != model.ThinkingEnabled {
			errs = append(errs, fmt.Errorf("capabilities.thinkingModes: %q is not an Anthropic thinking mode; use adaptive or enabled", m))
		}
	}

	for _, e := range d.EffortLevels {
		if !slices.Contains(effortAll, e) {
			errs = append(errs, fmt.Errorf("capabilities.effortLevels: %q is not an Anthropic effort level; use low, medium, high, xhigh or max", e))
		}
	}

	if d.ContextTokens < 0 {
		errs = append(errs, errors.New("capabilities.contextTokens: must not be negative"))
	}

	if d.MaxOutputTokens < 0 {
		errs = append(errs, errors.New("capabilities.maxOutputTokens: must not be negative"))
	}

	return errs
}

// capabilities converts the block to model.Capabilities.
func (d *DeclaredCapabilities) capabilities() model.Capabilities {
	return model.Capabilities{
		ToolCalling:      d.ToolCalling,
		ToolChoice:       d.ToolChoice,
		ThinkingModes:    d.ThinkingModes,
		EffortLevels:     d.EffortLevels,
		StructuredOutput: d.StructuredOutput,
		PromptCaching:    d.PromptCaching,
		ContextTokens:    d.ContextTokens,
		MaxOutputTokens:  d.MaxOutputTokens,
	}
}

// checkThinking checks the Anthropic rules for thinking that the shared
// settings check cannot know: whether the model can turn thinking off, and
// the size of a fixed budget. The mode and effort are assumed supported;
// Settings.Validate checks that.
func checkThinking(modelID string, m knownModel, t model.Thinking, effort model.Effort, maxTokens int) []error {
	var errs []error

	switch {
	case t.Mode == model.ThinkingOff && m.off == offRejected:
		errs = append(errs, fmt.Errorf("thinking.mode: %s always thinks and cannot turn thinking off; leave thinking out and lower effort instead", modelID))
	case t.Mode == model.ThinkingOff && m.off == offDisableUpToHigh && (effort == model.EffortXHigh || effort == model.EffortMax):
		errs = append(errs, fmt.Errorf("thinking.mode: off works on %s only with effort high or lower, not %s", modelID, effort))
	case t.Mode == model.ThinkingEnabled && t.BudgetTokens < minBudgetTokens:
		errs = append(errs, fmt.Errorf("thinking.budgetTokens: must be at least %d with mode enabled", minBudgetTokens))
	case t.Mode == model.ThinkingEnabled && t.BudgetTokens >= maxTokens:
		errs = append(errs, fmt.Errorf("thinking.budgetTokens: %d must be less than maxTokens (%d)", t.BudgetTokens, maxTokens))
	}

	return errs
}

// checkBaseURL checks that raw is an absolute https URL. Every call
// carries the API key, so plain http is allowed only to a loopback host,
// such as a local proxy.
func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("baseURL: %w", err)
	}

	switch {
	case (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return fmt.Errorf("baseURL: %q must be an absolute https URL", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https, since every call carries the API key; plain http is allowed only for localhost, 127.0.0.0/8 or ::1", raw)
	}

	return nil
}

// cmpOr returns the first of values that is above zero, or zero.
func cmpOr(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}

	return 0
}
