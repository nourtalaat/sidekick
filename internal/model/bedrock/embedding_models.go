package bedrock

import (
	"errors"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/model"
)

// embeddingFamily is the request and answer shape of an embedding model.
type embeddingFamily int

// Embedding model families.
const (
	// familyTitan is Amazon Titan Text Embeddings, one text per request.
	familyTitan embeddingFamily = iota
	// familyCohere is Cohere Embed, many texts per request.
	familyCohere
)

// embeddingModel is an embedding model the connector knows.
type embeddingModel struct {
	family embeddingFamily
	// dimensions are the vector lengths the model can return.
	dimensions []int
}

// knownEmbeddingModels are the embedding models on Bedrock this connector
// knows, by their bare model ID, from the Bedrock model parameter pages as
// of 2026-10-28. An inference profile ID such as us.cohere.embed-v4:0
// finds the same entry.
var knownEmbeddingModels = map[string]embeddingModel{
	"amazon.titan-embed-text-v2:0": {family: familyTitan, dimensions: []int{256, 512, 1024}},
	"cohere.embed-v4:0":            {family: familyCohere, dimensions: []int{256, 512, 1024, 1536}},
}

// Embedding implements model.EmbeddingConfigured. A config names an
// embedding model when it sets dimensions or names a model in the table of
// embedding models.
func (c *Config) Embedding() (dimensions int, ok bool) {
	_, known := lookupEmbeddingModel(c.Model)

	return c.Dimensions, c.Dimensions != 0 || known
}

// validateEmbedding checks the section of an embedding model without
// credentials or network calls: the region and endpoint, that the model is
// a known embedding model, that dimensions is one the model returns, and
// that none of the generation settings are set.
func (c *Config) validateEmbedding() error {
	var errs []error

	if !regionPattern.MatchString(c.Region) {
		errs = append(errs, fmt.Errorf("region: %q is not an AWS Region code such as us-east-1", c.Region))
	}

	if c.Endpoint != "" {
		if err := checkEndpoint(c.Endpoint); err != nil {
			errs = append(errs, err)
		}
	}

	errs = append(errs, c.checkGenerationSettings()...)

	if c.Model == "" {
		return errors.Join(append(errs, errors.New("model: required"))...)
	}

	known, isKnown := lookupEmbeddingModel(c.Model)

	switch {
	case !isKnown:
		errs = append(errs, fmt.Errorf("model: %q is not an embedding model the bedrock provider knows; use amazon.titan-embed-text-v2:0 or cohere.embed-v4:0", c.Model))
	case c.Dimensions == 0:
		errs = append(errs, fmt.Errorf("dimensions: required; %s returns %v", c.Model, known.dimensions))
	case !slices.Contains(known.dimensions, c.Dimensions):
		errs = append(errs, fmt.Errorf("dimensions: %d is not a length %s returns; use one of %v", c.Dimensions, c.Model, known.dimensions))
	}

	return errors.Join(errs...)
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
		{"capabilities", c.Declared != nil},
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

// lookupEmbeddingModel returns the table entry of a model ID or inference
// profile ID, and false when the model is not in the table.
func lookupEmbeddingModel(id string) (embeddingModel, bool) {
	m, ok := knownEmbeddingModels[baseModelID(id)]

	return m, ok
}
