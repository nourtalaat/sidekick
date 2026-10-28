package voyage

import (
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// defaultBaseURL is the address of the Voyage AI API.
const defaultBaseURL = "https://api.voyageai.com/v1"

// flexibleDimensions are the vector lengths the newer Voyage models can
// return, which the connector asks for with output_dimension.
var flexibleDimensions = []int{256, 512, 1024, 2048}

// knownModels are the Voyage embedding models the connector knows, from
// Voyage's embeddings API reference as of 2026-10-28.
var knownModels = []string{
	"voyage-4-large", "voyage-4", "voyage-4-lite",
	"voyage-3-large", "voyage-3.5", "voyage-3.5-lite",
	"voyage-code-3",
}

// Config is the config section of a Voyage AI connector. It is valid only
// as memory.vector.embeddings.
type Config struct {
	model.Settings `yaml:",inline"`

	// APIKeySecret holds the Voyage AI API key. It is required.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// BaseURL replaces the address of the API, for example for a proxy.
	// It must use https, except for a loopback host.
	BaseURL string `yaml:"baseURL"`
	// Dimensions is the length of the vectors, one of 256, 512, 1024 and
	// 2048. It is required.
	Dimensions int `yaml:"dimensions"`
}

var _ model.EmbeddingConfigured = (*Config)(nil)

// Embedding implements model.EmbeddingConfigured. Every Voyage config is an
// embedding config.
func (c *Config) Embedding() (dimensions int, ok bool) {
	return c.Dimensions, true
}

// Validate checks the section without secrets or network calls: the key,
// the address, that the model is known and that dimensions is a length it
// returns. Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.APIKeySecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("apiKeySecret: required"))
	}

	if c.BaseURL != "" {
		if err := checkBaseURL(c.BaseURL); err != nil {
			errs = append(errs, err)
		}
	}

	errs = append(errs, c.checkGenerationSettings()...)

	switch {
	case c.Model == "":
		errs = append(errs, errors.New("model: required"))
	case !slices.Contains(knownModels, c.Model):
		errs = append(errs, fmt.Errorf("model: %q is not a Voyage embedding model the voyage provider knows; use one of %v", c.Model, knownModels))
	}

	switch {
	case c.Dimensions == 0:
		errs = append(errs, fmt.Errorf("dimensions: required; use one of %v", flexibleDimensions))
	case !slices.Contains(flexibleDimensions, c.Dimensions):
		errs = append(errs, fmt.Errorf("dimensions: %d is not a length %s returns; use one of %v", c.Dimensions, c.Model, flexibleDimensions))
	}

	return errors.Join(errs...)
}

// address returns the base URL to call.
func (c *Config) address() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}

	return defaultBaseURL
}

// checkGenerationSettings returns an error for each setting that only
// means something for a generation model.
func (c *Config) checkGenerationSettings() []error {
	var errs []error

	for _, s := range []struct {
		key string
		set bool
	}{
		{"maxTokens", c.MaxTokens != 0},
		{"temperature", c.Temperature != nil},
		{"thinking", c.Thinking != (model.Thinking{})},
		{"effort", c.Effort != model.EffortDefault},
	} {
		if s.set {
			errs = append(errs, fmt.Errorf("%s: not used for embeddings", s.key))
		}
	}

	if c.Retries < 0 {
		errs = append(errs, errors.New("retries: must not be negative"))
	}

	return errs
}

// checkBaseURL checks that raw is an absolute https URL. Requests carry
// the API key, so plain http is allowed only to a loopback host.
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
