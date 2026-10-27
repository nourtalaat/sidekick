package openai

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// API is the OpenAI API a connector calls.
type API string

// APIs.
const (
	// APIResponses is the Responses API, the default for api.openai.com.
	APIResponses API = "responses"
	// APIChatCompletions is Chat Completions, the default when baseURL is
	// set, since OpenAI-compatible servers implement it most widely.
	APIChatCompletions API = "chat_completions"
)

// Server is the kind of OpenAI-compatible server at baseURL. It decides
// whether a preset model accepts a forced tool choice.
type Server string

// Servers.
const (
	// ServerUnknown is a server that is neither set nor inferred. It gets
	// no forced tool choice, the safe default.
	ServerUnknown  Server = ""
	ServerOllama   Server = "ollama"
	ServerVLLM     Server = "vllm"
	ServerLlamaCPP Server = "llamacpp"
)

// ollamaPort is the port Ollama listens on by default.
const ollamaPort = "11434"

// fineTunePrefix starts the IDs of OpenAI fine-tuned models, which hold
// colons without being Ollama tags.
const fineTunePrefix = "ft:"

var (
	knownAPIs    = []API{APIResponses, APIChatCompletions}
	knownServers = []Server{ServerOllama, ServerVLLM, ServerLlamaCPP}
)

// errThinking explains why every thinking mode is rejected.
var errThinking = errors.New("thinking: not accepted by the openai provider; OpenAI-style APIs control reasoning with effort instead (none turns it off where the model supports it)")

// Config is the config section of one OpenAI or OpenAI-compatible
// connector instance.
type Config struct {
	model.Settings `yaml:",inline"`

	// APIKeySecret holds the API key. It is required for api.openai.com
	// and optional with baseURL, since local servers often need none.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// BaseURL is the address of an OpenAI-compatible server, for example
	// http://ollama:11434/v1. Empty means api.openai.com.
	BaseURL string `yaml:"baseURL"`
	// API picks the API; empty means responses without baseURL and
	// chat_completions with it.
	API API `yaml:"api"`
	// Server is the kind of server at baseURL. Empty means inferred.
	Server Server `yaml:"server"`
	// Declared describes a model the connector does not know. It replaces
	// the known or preset capabilities when set.
	Declared *DeclaredCapabilities `yaml:"capabilities"`
}

var _ model.CapabilityDeclarer = (*Config)(nil)

// DeclaredCapabilities is the capabilities block of a model the connector
// does not know, in config.
type DeclaredCapabilities struct {
	ToolCalling bool `yaml:"toolCalling"`
	// ToolChoice says the server honours a forced tool choice. Ollama
	// does not.
	ToolChoice       bool           `yaml:"toolChoice"`
	EffortLevels     []model.Effort `yaml:"effortLevels"`
	StructuredOutput bool           `yaml:"structuredOutput"`
	PromptCaching    bool           `yaml:"promptCaching"`
	ContextTokens    int            `yaml:"contextTokens"`
	MaxOutputTokens  int            `yaml:"maxOutputTokens"`
}

// Capabilities implements model.CapabilityDeclarer: the declared block when
// there is one, otherwise the known OpenAI model, otherwise the local
// preset on the configured or inferred server, otherwise nothing.
func (c *Config) Capabilities() model.Capabilities {
	server, _ := c.ResolvedServer()
	caps, _ := c.known(server)

	return caps
}

// Validate checks the section without secrets or network calls: the API
// and server values, the key and address, that the model is known, a
// preset or declared, and that the shared settings fit its capabilities.
// Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	errs = append(errs, c.checkEndpoint()...)

	if c.Thinking != (model.Thinking{}) {
		errs = append(errs, errThinking)
	}

	server, _ := c.ResolvedServer()
	caps, ok := c.known(server)

	switch {
	case !ok && c.Model == "":
		errs = append(errs, errors.New("model: required"))

		return errors.Join(errs...)
	case !ok:
		errs = append(errs, fmt.Errorf("model: %q is not a model the openai provider knows; describe it in a capabilities block", c.Model))

		return errors.Join(errs...)
	}

	if c.Declared != nil {
		errs = append(errs, c.Declared.check()...)
	}

	// Thinking is checked above with a clearer message.
	shared := c.Settings
	shared.Thinking = model.Thinking{}

	if err := shared.Validate(caps); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// ResolvedServer returns the kind of server at baseURL: the server setting,
// or else the one inferred from the model ID and address, and whether it
// was set rather than inferred. sidekick doctor reports a server that was
// not set.
func (c *Config) ResolvedServer() (server Server, set bool) {
	if c.Server != ServerUnknown {
		return c.Server, true
	}

	return c.inferServer(), false
}

// MinContextTokens returns the smallest context the server should run the
// configured model with, or zero when the model is not a preset.
func (c *Config) MinContextTokens() int {
	if c.BaseURL == "" || c.Declared != nil {
		return 0
	}

	p, _, ok := presetFor(c.Model)
	if !ok {
		return 0
	}

	return p.minContextTokens
}

// api returns the API the connector calls.
func (c *Config) api() API {
	switch {
	case c.API != "":
		return c.API
	case c.BaseURL != "":
		return APIChatCompletions
	default:
		return APIResponses
	}
}

// maxTokens returns the output limit of a call: the request's, else the
// config's, capped at the model's maximum. Zero leaves the limit out, so
// the model's or server's own applies.
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

// checkEndpoint checks the API, server, base URL and key settings.
func (c *Config) checkEndpoint() []error {
	var errs []error

	if c.API != "" && !slices.Contains(knownAPIs, c.API) {
		errs = append(errs, fmt.Errorf("api: %q is not an API; use responses or chat_completions", c.API))
	}

	if c.Server != ServerUnknown && !slices.Contains(knownServers, c.Server) {
		errs = append(errs, fmt.Errorf("server: %q is not a server kind; use ollama, vllm or llamacpp", c.Server))
	}

	if c.BaseURL == "" {
		if c.Server != ServerUnknown {
			errs = append(errs, errors.New("server: only applies with baseURL"))
		}

		if c.APIKeySecret == (registry.SecretRef{}) {
			errs = append(errs, errors.New("apiKeySecret: required without baseURL"))
		}

		return errs
	}

	if err := checkBaseURL(c.BaseURL, c.APIKeySecret != (registry.SecretRef{})); err != nil {
		errs = append(errs, err)
	}

	return errs
}

// known returns the capabilities of the configured model on server, and
// false when the connector knows nothing about it. Presets apply only with
// baseURL, since api.openai.com does not serve them.
func (c *Config) known(server Server) (model.Capabilities, bool) {
	if c.Declared != nil {
		return c.Declared.capabilities(), true
	}

	if caps, ok := knownModels[c.Model]; ok {
		return caps, true
	}

	if c.BaseURL == "" {
		return model.Capabilities{}, false
	}

	p, _, ok := presetFor(c.Model)
	if !ok {
		return model.Capabilities{}, false
	}

	return p.capabilities(server), true
}

// inferServer infers the server kind with baseURL set: Ollama when the
// model ID is an Ollama tag or a preset's Ollama name, or the address uses
// Ollama's port; otherwise unknown.
func (c *Config) inferServer() Server {
	if c.BaseURL == "" {
		return ServerUnknown
	}

	if isOllamaTag(c.Model) {
		return ServerOllama
	}

	if _, viaOllama, ok := presetFor(c.Model); ok && viaOllama {
		return ServerOllama
	}

	if u, err := url.Parse(c.BaseURL); err == nil && u.Port() == ollamaPort {
		return ServerOllama
	}

	return ServerUnknown
}

// check returns problems with a capabilities block.
func (d *DeclaredCapabilities) check() []error {
	var errs []error

	for _, e := range d.EffortLevels {
		if !slices.Contains(effortAll, e) {
			errs = append(errs, fmt.Errorf("capabilities.effortLevels: %q is not an OpenAI effort level; use none, minimal, low, medium, high, xhigh or max", e))
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
		EffortLevels:     d.EffortLevels,
		StructuredOutput: d.StructuredOutput,
		PromptCaching:    d.PromptCaching,
		ContextTokens:    d.ContextTokens,
		MaxOutputTokens:  d.MaxOutputTokens,
	}
}

// isOllamaTag reports whether a model ID has Ollama's name:tag form, such
// as qwen3.8:27b. OpenAI fine-tune IDs hold colons too and are not tags.
func isOllamaTag(id string) bool {
	if strings.HasPrefix(id, fineTunePrefix) || strings.Count(id, ":") != 1 {
		return false
	}

	name, tag, _ := strings.Cut(id, ":")

	return name != "" && tag != ""
}

// checkBaseURL checks that raw is an absolute http or https URL. With a
// key, raw must use https unless its host is loopback, so the key never
// crosses the network unencrypted. Without a key, plain http stays allowed
// for a compatible server on another host, since no credential is sent.
func checkBaseURL(raw string, withKey bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("baseURL: %w", err)
	}

	switch {
	case (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return fmt.Errorf("baseURL: %q must be an absolute http or https URL", raw)
	case withKey && u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https when apiKeySecret is set; plain http is allowed only for localhost, 127.0.0.0/8 or ::1, or without apiKeySecret", raw)
	}

	return nil
}
