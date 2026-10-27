package google

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Backend is the Google service a connector calls.
type Backend string

// Backends.
const (
	// BackendGemini is the Gemini API (Google AI Studio), with an API key.
	BackendGemini Backend = "gemini"
	// BackendVertex is Vertex AI, now named Gemini Enterprise Agent
	// Platform, with Application Default Credentials.
	BackendVertex Backend = "vertex"
)

// defaultLocation is the Vertex AI location used when none is set. The
// preview models are served only there.
const defaultLocation = "global"

var knownBackends = []Backend{BackendGemini, BackendVertex}

// Patterns of the values the connector puts in request paths.
var (
	// projectPattern matches a Google Cloud project ID, including the
	// older domain-scoped form example.com:project.
	projectPattern = regexp.MustCompile(`^[a-z][a-z0-9.:-]{4,61}[a-z0-9]$`)
	// locationPattern matches a location such as global, us, eu or
	// europe-west4.
	locationPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)
)

// errBudgetAndEffort explains why a capabilities block cannot declare both.
var errBudgetAndEffort = errors.New("capabilities: a Gemini model takes either effort levels (Gemini 3 and later) or a thinking budget (thinkingModes with enabled, Gemini 2.5), not both")

// Config is the config section of one Google connector instance.
type Config struct {
	model.Settings `yaml:",inline"`

	// Backend is gemini (the default) or vertex.
	Backend Backend `yaml:"backend"`
	// APIKeySecret holds the Gemini API key. It is required with backend
	// gemini and not accepted with vertex.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// Project is the Google Cloud project ID, required with vertex.
	Project string `yaml:"project"`
	// Location is the Vertex AI location, for example global or
	// europe-west4. Empty means global.
	Location string `yaml:"location"`
	// BaseURL replaces the API address, for example for a proxy or a
	// Private Service Connect endpoint.
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

// Validate checks the section without credentials or network calls: the
// backend and its settings, the base URL, that the model is known or
// declared, that the shared settings fit its capabilities, and the Gemini
// rules for thinking. Each error starts with the key of its field.
func (c *Config) Validate() error {
	errs := c.checkBackend()

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
		errs = append(errs, fmt.Errorf("model: %q is not a model the google provider knows; describe it in a capabilities block", c.Model))

		return errors.Join(errs...)
	}

	if c.Declared != nil {
		errs = append(errs, c.Declared.check()...)
	}

	if err := c.Settings.Validate(m.caps); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, checkThinking(c.Model, m, c.Thinking)...)

	return errors.Join(errs...)
}

// backend returns the configured backend, gemini when none is set.
func (c *Config) backend() Backend {
	if c.Backend == "" {
		return BackendGemini
	}

	return c.Backend
}

// location returns the Vertex AI location, global when none is set.
func (c *Config) location() string {
	if c.Location == "" {
		return defaultLocation
	}

	return c.Location
}

// baseURL returns the address to call: the baseURL setting, or the
// backend's own address for the location. It is always set, so the SDK
// never takes one from GOOGLE_GEMINI_BASE_URL or GOOGLE_VERTEX_BASE_URL.
func (c *Config) baseURL() string {
	switch {
	case c.BaseURL != "":
		return c.BaseURL
	case c.backend() == BackendGemini:
		return geminiBaseURL
	case c.location() == defaultLocation:
		return vertexGlobalBaseURL
	case multiRegions[c.location()]:
		return "https://aiplatform." + c.location() + ".rep.googleapis.com/"
	default:
		return "https://" + c.location() + "-aiplatform.googleapis.com/"
	}
}

// known returns what the connector knows about the configured model, and
// false when it knows nothing: the model is neither declared nor in the
// table.
func (c *Config) known() (knownModel, bool) {
	if c.Declared != nil {
		return c.Declared.knownModel(), true
	}

	m, ok := knownModels[c.Model]

	return m, ok
}

// maxTokens returns the output limit of a call: the request's, else the
// config's, capped at the model's maximum. Zero leaves the limit to the
// model.
func (c *Config) maxTokens(requested int, caps model.Capabilities) int {
	n := requested
	if n <= 0 {
		n = c.MaxTokens
	}

	if caps.MaxOutputTokens > 0 && n > caps.MaxOutputTokens {
		return caps.MaxOutputTokens
	}

	return max(n, 0)
}

// checkBackend checks the backend and the settings that belong to it.
func (c *Config) checkBackend() []error {
	var errs []error

	switch c.backend() {
	case BackendGemini:
		if c.APIKeySecret == (registry.SecretRef{}) {
			errs = append(errs, errors.New("apiKeySecret: required with backend gemini"))
		}

		if c.Project != "" {
			errs = append(errs, errors.New("project: only applies with backend vertex"))
		}

		if c.Location != "" {
			errs = append(errs, errors.New("location: only applies with backend vertex"))
		}
	case BackendVertex:
		if c.APIKeySecret != (registry.SecretRef{}) {
			errs = append(errs, errors.New("apiKeySecret: not accepted with backend vertex, which uses Application Default Credentials such as GKE Workload Identity"))
		}

		if !projectPattern.MatchString(c.Project) {
			errs = append(errs, fmt.Errorf("project: %q is not a Google Cloud project ID", c.Project))
		}

		if c.Location != "" && !locationPattern.MatchString(c.Location) {
			errs = append(errs, fmt.Errorf("location: %q is not a location such as global or europe-west4", c.Location))
		}
	default:
		errs = append(errs, fmt.Errorf("backend: %q is not a backend; use %s or %s", c.Backend, knownBackends[0], knownBackends[1]))
	}

	return errs
}

// check returns problems with a capabilities block. Effort is sent as a
// thinking level and a fixed budget as a thinking budget, and a Gemini
// model takes only one of the two.
func (d *DeclaredCapabilities) check() []error {
	var errs []error

	for _, m := range d.ThinkingModes {
		if m != model.ThinkingAdaptive && m != model.ThinkingEnabled {
			errs = append(errs, fmt.Errorf("capabilities.thinkingModes: %q is not a Gemini thinking mode; use adaptive or enabled", m))
		}
	}

	for _, e := range d.EffortLevels {
		if !slices.Contains(effortAll, e) {
			errs = append(errs, fmt.Errorf("capabilities.effortLevels: %q is not a Gemini thinking level; use minimal, low, medium or high", e))
		}
	}

	if len(d.EffortLevels) > 0 && slices.Contains(d.ThinkingModes, model.ThinkingEnabled) {
		errs = append(errs, errBudgetAndEffort)
	}

	if d.ContextTokens < 0 {
		errs = append(errs, errors.New("capabilities.contextTokens: must not be negative"))
	}

	if d.MaxOutputTokens < 0 {
		errs = append(errs, errors.New("capabilities.maxOutputTokens: must not be negative"))
	}

	return errs
}

// knownModel converts the block to a table entry. A model that declares a
// fixed budget takes thinking budgets, and its thinking may be turned off;
// any other declared model takes thinking levels.
func (d *DeclaredCapabilities) knownModel() knownModel {
	m := knownModel{caps: model.Capabilities{
		ToolCalling:      d.ToolCalling,
		ToolChoice:       d.ToolChoice,
		ThinkingModes:    d.ThinkingModes,
		EffortLevels:     d.EffortLevels,
		StructuredOutput: d.StructuredOutput,
		PromptCaching:    d.PromptCaching,
		ContextTokens:    d.ContextTokens,
		MaxOutputTokens:  d.MaxOutputTokens,
	}}

	if slices.Contains(d.ThinkingModes, model.ThinkingEnabled) {
		m.budget = &budgetRange{min: 1, max: d.MaxOutputTokens}
		m.canTurnOff = true
	}

	return m
}

// checkThinking checks the Gemini rules for thinking that the shared
// settings check cannot know: whether the model can turn thinking off, and
// the size of a fixed budget. The mode is assumed supported;
// Settings.Validate checks that.
func checkThinking(modelID string, m knownModel, t model.Thinking) []error {
	var errs []error

	switch {
	case t.Mode == model.ThinkingOff && !m.canTurnOff:
		errs = append(errs, fmt.Errorf("thinking.mode: %s always thinks and cannot turn thinking off; leave thinking out and lower effort instead", modelID))
	case t.Mode == model.ThinkingEnabled && m.budget != nil && t.BudgetTokens < m.budget.min:
		errs = append(errs, fmt.Errorf("thinking.budgetTokens: must be at least %d on %s", m.budget.min, modelID))
	case t.Mode == model.ThinkingEnabled && m.budget != nil && m.budget.max > 0 && t.BudgetTokens > m.budget.max:
		errs = append(errs, fmt.Errorf("thinking.budgetTokens: must be at most %d on %s", m.budget.max, modelID))
	}

	return errs
}

// checkBaseURL checks that raw is an absolute https URL. Requests carry the
// credentials, alert text and tool output, so plain http is allowed only to
// a loopback host, such as a local proxy.
func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("baseURL: %w", err)
	}

	switch {
	case u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return fmt.Errorf("baseURL: %q must be an absolute https URL", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is allowed only for localhost, 127.0.0.0/8 or ::1", raw)
	}

	return nil
}
