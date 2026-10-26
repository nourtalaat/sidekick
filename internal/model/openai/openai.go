package openai

import (
	"context"
	"errors"
	"fmt"
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

// requestIDHeader is the response header that carries OpenAI's request ID.
const requestIDHeader = "x-request-id"

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

// Connector calls one configured model on the OpenAI API or on an
// OpenAI-compatible server.
type Connector struct {
	name      string
	cfg       Config
	caps      model.Capabilities
	api       API
	responses responses.ResponseService
	chat      openai.ChatCompletionService
	secrets   registry.Secrets
}

var _ model.Connector = (*Connector)(nil)

// Capabilities reports what the configured model supports.
func (c *Connector) Capabilities() model.Capabilities {
	return c.caps
}

// ModelID returns the configured model ID, for telemetry labels.
func (c *Connector) ModelID() string {
	return c.cfg.Model
}

// Generate runs one model turn. Both APIs are streamed, so a long answer
// does not hit the HTTP client's wait for response headers, and collected
// into one response. Provider failures are returned as a *model.Error; a
// request the model cannot take is an invalid_request error before any
// network call.
func (c *Connector) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	if err := c.checkRequest(req); err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, err)}
	}

	opts, err := c.callOptions(ctx)
	if err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrAuth, Err: fmt.Errorf("connector %s: read API key: %w", c.name, err)}
	}

	ctx, cancel := context.WithTimeout(ctx, cmpOrDuration(c.cfg.Timeout, defaultTimeout))
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

// callOptions returns the per-call request options: the API key, read on
// every call so a rotated key is used without a restart. A connector with
// no key sends no Authorization header.
func (c *Connector) callOptions(ctx context.Context) ([]option.RequestOption, error) {
	if c.cfg.APIKeySecret == (registry.SecretRef{}) {
		return nil, nil
	}

	key, err := secrets.ResolveText(ctx, c.secrets, c.cfg.APIKeySecret)
	if err != nil {
		return nil, fmt.Errorf("resolve %s/%s: %w", c.cfg.APIKeySecret.Name, c.cfg.APIKeySecret.Key, err)
	}

	return []option.RequestOption{option.WithAPIKey(key)}, nil
}

// New builds a connector from its validated config. When the config names
// an API key, New reads it once, so a missing secret stops start-up.
//
// The services are built from explicit options rather than
// openai.NewClient, which would read OPENAI_API_KEY, OPENAI_BASE_URL and
// other variables from the environment.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (model.Connector, error) {
	if cfg.APIKeySecret != (registry.SecretRef{}) {
		if deps.Secrets == nil {
			return nil, errNoSecrets
		}

		if _, err := secrets.ResolveText(ctx, deps.Secrets, cfg.APIKeySecret); err != nil {
			return nil, fmt.Errorf("read API key: %w", err)
		}
	}

	server, _ := cfg.ResolvedServer()

	caps, ok := cfg.known(server)
	if !ok {
		return nil, fmt.Errorf("model %q: %w", cfg.Model, errUnknownModel)
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = openAIBaseURL
	}

	opts := []option.RequestOption{
		option.WithHTTPClient(httpClient),
		option.WithBaseURL(baseURL),
		option.WithMaxRetries(0),
	}

	return &Connector{
		name:      name,
		cfg:       cfg,
		caps:      caps,
		api:       cfg.api(),
		responses: responses.NewResponseService(opts...),
		chat:      openai.NewChatCompletionService(opts...),
		secrets:   deps.Secrets,
	}, nil
}

// cmpOrDuration returns d, or def when d is not above zero.
func cmpOrDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}

	return def
}
