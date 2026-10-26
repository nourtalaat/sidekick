// Package anthropic is the model connector for the Anthropic Messages API.
// It registers itself as the model provider "anthropic".
//
// The connector streams every call, so long outputs and long thinking do
// not hit HTTP timeouts, and returns one model.Response. It reports token
// usage only; callers price it with the pricing package. The SDK's own
// retries are off, since model.Generate retries for every connector.
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Provider is the model provider name this package registers.
const Provider = "anthropic"

// defaultTimeout bounds one call when the timeout setting is zero. The
// Anthropic SDK suggests ten minutes for streamed calls.
const defaultTimeout = 10 * time.Minute

// requestIDHeader is the response header that carries Anthropic's request ID.
const requestIDHeader = "request-id"

// errNoSecrets is returned when the connector is built without a way to
// read its API key.
var errNoSecrets = errors.New("no secrets resolver in deps")

// errUnknownModel is returned by New for a model that is neither known nor
// declared. Validate reports the same problem with more detail.
var errUnknownModel = errors.New("not a known model and no capabilities block")

// errIncomplete is returned when the stream ended before the message did.
var errIncomplete = errors.New("stream ended before the message was complete")

func init() {
	registry.Models.Register(Provider, registry.Typed(New))
}

// Connector calls one configured Claude model.
type Connector struct {
	name    string
	cfg     Config
	model   knownModel
	client  anthropic.Client
	secrets registry.Secrets
}

var _ model.Connector = (*Connector)(nil)

// Capabilities reports what the configured model supports.
func (c *Connector) Capabilities() model.Capabilities {
	return c.model.caps
}

// ModelID returns the configured model ID, for telemetry labels.
func (c *Connector) ModelID() string {
	return c.cfg.Model
}

// Generate runs one model turn. Provider failures are returned as a
// *model.Error; a request the model cannot take is an invalid_request
// error before any network call.
func (c *Connector) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	params, err := c.params(req)
	if err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, err)}
	}

	key, err := secrets.ResolveText(ctx, c.secrets, c.cfg.APIKeySecret)
	if err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrAuth, Err: fmt.Errorf("connector %s: read API key: %w", c.name, err)}
	}

	ctx, cancel := context.WithTimeout(ctx, cmpOrDuration(c.cfg.Timeout, defaultTimeout))
	defer cancel()

	start := time.Now()

	msg, requestID, err := c.stream(ctx, params, key)
	if err != nil {
		return model.Response{}, classify(fmt.Errorf("connector %s: %w", c.name, err))
	}

	resp := response(msg)
	resp.Latency = time.Since(start)
	resp.RequestID = requestID

	return resp, nil
}

// stream sends params as a streamed request and collects the events into
// one message. It also returns the request ID from the response headers.
func (c *Connector) stream(ctx context.Context, params anthropic.MessageNewParams, key string) (msg anthropic.Message, requestID string, err error) {
	var httpResp *http.Response

	s := c.client.Messages.NewStreaming(ctx, params, option.WithAPIKey(key), option.WithResponseInto(&httpResp))

	defer func() {
		if cerr := s.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close stream: %w", cerr)
		}
	}()

	if httpResp != nil {
		requestID = httpResp.Header.Get(requestIDHeader)
	}

	for s.Next() {
		if aerr := msg.Accumulate(s.Current()); aerr != nil {
			return msg, requestID, fmt.Errorf("read stream: %w", aerr)
		}
	}

	if serr := s.Err(); serr != nil {
		return msg, requestID, fmt.Errorf("stream messages: %w", serr)
	}

	if msg.StopReason == "" {
		return msg, requestID, errIncomplete
	}

	return msg, requestID, nil
}

// New builds a connector from its validated config. It reads the API key
// once, so a missing secret stops start-up, and again on every call, so a
// rotated key is used without a restart.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (model.Connector, error) {
	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	if _, err := secrets.ResolveText(ctx, deps.Secrets, cfg.APIKeySecret); err != nil {
		return nil, fmt.Errorf("read API key: %w", err)
	}

	m, ok := cfg.known()
	if !ok {
		return nil, fmt.Errorf("model %q: %w", cfg.Model, errUnknownModel)
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithHTTPClient(httpClient),
		option.WithMaxRetries(0),
		option.WithOpenTelemetryTracesContentMode(option.OpenTelemetryContentModeMetadataOnly),
	}

	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}

	return &Connector{
		name:    name,
		cfg:     cfg,
		model:   m,
		client:  anthropic.NewClient(opts...),
		secrets: deps.Secrets,
	}, nil
}

// cmpOrDuration returns d, or def when d is not above zero.
func cmpOrDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}

	return def
}
