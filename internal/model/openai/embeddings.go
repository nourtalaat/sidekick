package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/nourtalaat/sidekick/internal/model"
)

// defaultEmbedTimeout bounds one embedding call when the timeout setting
// is zero. Embedding a batch is far quicker than a model turn.
const defaultEmbedTimeout = time.Minute

// errEmbeddingOnly is returned by Generate on a connector built for an
// embedding model.
var errEmbeddingOnly = errors.New("the connector is built for an embedding model and cannot generate")

// errNotEmbedding is returned by Embed on a connector built for a
// generation model.
var errNotEmbedding = errors.New("the connector is not set up for embeddings: use an embedding model and set dimensions")

// errWrongVectorLength is returned when the server answers with a vector
// whose length is not the configured dimensions.
var errWrongVectorLength = errors.New("the server returned a vector of the wrong length")

// errBadAnswer is returned when the answer does not hold one vector for
// each text.
var errBadAnswer = errors.New("the server did not return one vector for each text")

// embeddingModel is an OpenAI embedding model the connector knows.
type embeddingModel struct {
	// native is the length of the model's vectors.
	native int
	// flexible is true when the model can return shorter vectors, which
	// the connector asks for with the dimensions parameter.
	flexible bool
}

// knownEmbeddingModels are the OpenAI embedding models, from OpenAI's API
// reference as of 2026-10-28. Another model on a server at baseURL is
// accepted with the dimensions it declares.
var knownEmbeddingModels = map[string]embeddingModel{
	"text-embedding-3-small": {native: 1536, flexible: true},
	"text-embedding-3-large": {native: 3072, flexible: true},
	"text-embedding-ada-002": {native: 1536},
}

// Embedding implements model.EmbeddingConfigured. A config names an
// embedding model when it sets dimensions or names a model in the table of
// embedding models.
func (c *Config) Embedding() (dimensions int, ok bool) {
	_, known := knownEmbeddingModels[c.Model]

	return c.Dimensions, c.Dimensions != 0 || known
}

// Embed returns one vector for each text, in order. Provider failures are
// returned as a *model.Error. The vectors are as long as the configured
// dimensions, or the call fails with an invalid_request error.
func (c *Connector) Embed(ctx context.Context, texts []string) (model.EmbedResult, error) {
	if c.dimensions == 0 {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, errNotEmbedding)}
	}

	opts, err := c.credentials(ctx)
	if err != nil {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrAuth, Err: fmt.Errorf("connector %s: get credentials: %w", c.name, err)}
	}

	ctx, cancel := context.WithTimeout(ctx, cmpOrDuration(c.settings.Timeout, defaultEmbedTimeout))
	defer cancel()

	var httpResp *http.Response

	resp, err := c.embeddings.New(ctx, c.embedParams(texts), append(opts, option.WithResponseInto(&httpResp))...)
	if err != nil {
		return model.EmbedResult{}, classify(fmt.Errorf("connector %s: create embeddings: %w", c.name, err))
	}

	vectors, err := c.vectors(resp, len(texts))
	if err != nil {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, err)}
	}

	out := model.EmbedResult{
		Vectors: vectors,
		Usage:   model.Usage{InputTokens: resp.Usage.PromptTokens},
	}

	if httpResp != nil {
		out.RequestID = requestIDOf(httpResp.Header)
	}

	return out, nil
}

// Dimensions returns the length of the vectors Embed returns, or zero for a
// connector that is not set up for embeddings.
func (c *Connector) Dimensions() int {
	return c.dimensions
}

// embedParams builds the embeddings request for texts.
func (c *Connector) embedParams(texts []string) openai.EmbeddingNewParams {
	p := openai.EmbeddingNewParams{
		Model:          c.model,
		Input:          openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
		EncodingFormat: openai.EmbeddingNewParamsEncodingFormatFloat,
	}

	if c.sendDimensions {
		p.Dimensions = openai.Int(int64(c.dimensions))
	}

	return p
}

// vectors returns the vectors of resp in the order of the texts, checking
// that there is one for each text and that each has the configured length.
func (c *Connector) vectors(resp *openai.CreateEmbeddingResponse, want int) ([][]float32, error) {
	if len(resp.Data) != want {
		return nil, fmt.Errorf("%w: sent %d texts, got %d vectors", errBadAnswer, want, len(resp.Data))
	}

	out := make([][]float32, want)

	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= int64(want) || out[d.Index] != nil {
			return nil, fmt.Errorf("%w: vector index %d is out of range or repeated", errBadAnswer, d.Index)
		}

		if len(d.Embedding) != c.dimensions {
			return nil, fmt.Errorf("%w: expected %d numbers, got %d; set dimensions to what model %s returns", errWrongVectorLength, c.dimensions, len(d.Embedding), c.model)
		}

		v := make([]float32, len(d.Embedding))
		for i, f := range d.Embedding {
			v[i] = float32(f)
		}

		out[d.Index] = v
	}

	return out, nil
}

// validateEmbedding checks the section of an embedding model without
// secrets or network calls: the endpoint and key, that the model is a known
// embedding model or declared with its dimensions, and that none of the
// generation settings are set.
func (c *Config) validateEmbedding() error {
	errs := c.checkEndpoint()

	errs = append(errs, c.checkGenerationSettings()...)

	if c.Model == "" {
		return errors.Join(append(errs, errors.New("model: required"))...)
	}

	known, isKnown := knownEmbeddingModels[c.Model]

	switch _, isChat := knownModels[c.Model]; {
	case isChat:
		errs = append(errs, fmt.Errorf("model: %q is a generation model; memory.vector.embeddings needs an embedding model such as text-embedding-3-small", c.Model))
	case !isKnown && c.BaseURL == "":
		errs = append(errs, fmt.Errorf("model: %q is not an embedding model OpenAI offers; use one of %v", c.Model, knownEmbeddingNames()))
	}

	switch {
	case c.Dimensions <= 0:
		errs = append(errs, errors.New("dimensions: required, the length of the vectors the model returns"))
	case isKnown && known.flexible && c.Dimensions > known.native:
		errs = append(errs, fmt.Errorf("dimensions: %s returns at most %d", c.Model, known.native))
	case isKnown && !known.flexible && c.Dimensions != known.native:
		errs = append(errs, fmt.Errorf("dimensions: %s always returns %d", c.Model, known.native))
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
		{"api", c.API != ""},
		{"server", c.Server != ServerUnknown},
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

// knownEmbeddingNames returns the known embedding model names, sorted.
func knownEmbeddingNames() []string {
	names := make([]string, 0, len(knownEmbeddingModels))
	for n := range knownEmbeddingModels {
		names = append(names, n)
	}

	slices.Sort(names)

	return names
}
