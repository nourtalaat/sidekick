package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Provider is the model provider name this package registers.
const Provider = "openai"

// defaultTimeout bounds one call when the timeout setting is zero. Long
// reasoning, and local models on modest hardware, can take minutes.
const defaultTimeout = 10 * time.Minute

// Response headers that carry the request ID: OpenAI's, then the one
// Azure's API gateway adds.
const (
	requestIDHeader = "x-request-id"
	apimRequestID   = "apim-request-id"
)

// openAIBaseURL is the address of the OpenAI API.
const openAIBaseURL = "https://api.openai.com/v1/"

// errUnknownModel is returned by New for a model that is neither known, a
// preset nor declared. Validate reports the same problem with more detail.
var errUnknownModel = errors.New("not a known model or preset and no capabilities block")

// errNoSecrets is returned when the connector needs an API key and is
// built without a way to read it.
var errNoSecrets = errors.New("no secrets resolver in deps")

func init() {
	registry.Models.Register(Provider, registry.Typed(New))
}

// Connector calls one configured model on the OpenAI API, on Azure or on
// an OpenAI-compatible server.
type Connector struct {
	name     string
	settings model.Settings
	// provider is the provider name the connector registered under, kept
	// in the Reasoning parts it returns: reasoning from OpenAI and from
	// Azure cannot be sent to the other.
	provider string
	// model is the name sent as the request's model: the model ID, or an
	// Azure deployment name.
	model string
	caps  model.Capabilities
	api   API
	// maxCompletionTokens sends Chat Completions' output limit as
	// max_completion_tokens, which OpenAI's own API wants, rather than
	// max_tokens, which compatible servers widely take.
	maxCompletionTokens bool
	// credentials returns the per-call request options that carry the
	// credentials, read on every call so a rotated key or a refreshed
	// token is used without a restart.
	credentials func(ctx context.Context) ([]option.RequestOption, error)
	responses   responses.ResponseService
	chat        openai.ChatCompletionService
	embeddings  openai.EmbeddingService
	// dimensions is the length of the vectors of an embedding connector,
	// and zero for a generation connector.
	dimensions int
	// sendDimensions asks the server for vectors of dimensions numbers,
	// which only a model that can return shorter vectors takes.
	sendDimensions bool
}

var (
	_ model.Connector = (*Connector)(nil)
	_ model.Embedder  = (*Connector)(nil)
)

// Capabilities reports what the configured model supports.
func (c *Connector) Capabilities() model.Capabilities {
	return c.caps
}

// ModelID returns the configured model ID, for telemetry labels.
func (c *Connector) ModelID() string {
	return c.settings.Model
}

// Generate runs one model turn. Both APIs are streamed, so a long answer
// does not hit the HTTP client's wait for response headers, and collected
// into one response. Provider failures are returned as a *model.Error; a
// request the model cannot take is an invalid_request error before any
// network call.
func (c *Connector) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	if c.dimensions != 0 {
		return model.Response{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, errEmbeddingOnly)}
	}

	if err := c.checkRequest(req); err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, err)}
	}

	opts, err := c.credentials(ctx)
	if err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrAuth, Err: fmt.Errorf("connector %s: get credentials: %w", c.name, err)}
	}

	ctx, cancel := context.WithTimeout(ctx, cmpOrDuration(c.settings.Timeout, defaultTimeout))
	defer cancel()

	start := time.Now()

	var resp model.Response

	switch c.api {
	case APIChatCompletions:
		resp, err = c.generateChat(ctx, req, opts)
	default:
		resp, err = c.generateResponses(ctx, req, opts)
	}

	if err != nil {
		return model.Response{}, classify(fmt.Errorf("connector %s: %w", c.name, err))
	}

	resp.Latency = time.Since(start)

	return resp, nil
}

// New builds a connector from its validated config. When the config names
// an API key, New reads it once, so a missing secret stops start-up.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (model.Connector, error) {
	if cfg.APIKeySecret != (registry.SecretRef{}) {
		if deps.Secrets == nil {
			return nil, errNoSecrets
		}

		if _, err := secrets.ResolveText(ctx, deps.Secrets, cfg.APIKeySecret); err != nil {
			return nil, fmt.Errorf("read API key: %w", err)
		}
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = openAIBaseURL
	}

	c := &Connector{
		name:                name,
		settings:            cfg.Settings,
		provider:            Provider,
		model:               cfg.Model,
		maxCompletionTokens: cfg.BaseURL == "",
		credentials:         bearerKey(deps.Secrets, cfg.APIKeySecret),
	}

	if dims, embedding := cfg.Embedding(); embedding {
		known, isKnown := knownEmbeddingModels[cfg.Model]
		c.dimensions = dims
		c.sendDimensions = isKnown && known.flexible
	} else {
		server, _ := cfg.ResolvedServer()

		caps, ok := cfg.known(server)
		if !ok {
			return nil, fmt.Errorf("model %q: %w", cfg.Model, errUnknownModel)
		}

		c.caps = caps
		c.api = cfg.api()
	}

	c.connect(deps.HTTP, baseURL)

	return c, nil
}

// connect builds the API services for baseURL. They are built from
// explicit options rather than openai.NewClient, which would read
// OPENAI_API_KEY, OPENAI_BASE_URL and other variables from the
// environment. The SDK's own retries are off, since model.Generate retries
// for every connector.
func (c *Connector) connect(httpClient *http.Client, baseURL string) {
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	opts := []option.RequestOption{
		option.WithHTTPClient(httpClient),
		option.WithBaseURL(baseURL),
		option.WithMaxRetries(0),
	}

	c.responses = responses.NewResponseService(opts...)
	c.chat = openai.NewChatCompletionService(opts...)
	c.embeddings = openai.NewEmbeddingService(opts...)
}

// bearerKey returns the credentials of a connector that sends the API key
// in ref as a bearer token. With no key it sends no Authorization header.
func bearerKey(s registry.Secrets, ref registry.SecretRef) func(ctx context.Context) ([]option.RequestOption, error) {
	return func(ctx context.Context) ([]option.RequestOption, error) {
		if ref == (registry.SecretRef{}) {
			return nil, nil
		}

		key, err := secrets.ResolveText(ctx, s, ref)
		if err != nil {
			return nil, fmt.Errorf("resolve %s/%s: %w", ref.Name, ref.Key, err)
		}

		return []option.RequestOption{option.WithAPIKey(key)}, nil
	}
}

// requestIDOf returns the request ID in the response headers, or "".
func requestIDOf(h http.Header) string {
	if id := h.Get(requestIDHeader); id != "" {
		return id
	}

	return h.Get(apimRequestID)
}

// cmpOrDuration returns d, or def when d is not above zero.
func cmpOrDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}

	return def
}
