// Package voyage is the embeddings connector for Voyage AI. It registers
// itself as the model provider "voyage". It only embeds: it implements
// model.Embedder, and it is valid only as memory.vector.embeddings.
//
// The connector calls POST /embeddings with the API key from a Secret,
// read on every call so a rotated key is used without a restart. Voyage's
// own truncation of long inputs stays on, and no input type is sent, so
// stored texts and search texts are embedded the same way.
package voyage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Provider is the model provider name this package registers.
const Provider = "voyage"

// defaultTimeout bounds one call when the timeout setting is zero.
const defaultTimeout = time.Minute

// maxResponseSize bounds one response body. A batch of 64 vectors of 2048
// numbers is about 2 MB of JSON.
const maxResponseSize = 16 << 20

// maxErrorBody is the most of an error response body the connector keeps
// in an error message.
const maxErrorBody = 1 << 10

// Errors of embedding calls.
var (
	errGenerate           = errors.New("the voyage provider only embeds text and cannot generate")
	errNoSecrets          = errors.New("no secrets resolver in deps")
	errResponseTooLarge   = fmt.Errorf("voyage sent a response larger than %d bytes", maxResponseSize)
	errWrongVectorLength  = errors.New("the server returned a vector of the wrong length")
	errBadAnswer          = errors.New("the server did not return one vector for each text")
	errUnexpectedResponse = errors.New("unexpected response")
)

func init() {
	registry.Models.Register(Provider, registry.Typed(New))
}

// Connector embeds text with one Voyage AI model.
type Connector struct {
	name       string
	cfg        Config
	secrets    registry.Secrets
	httpClient *http.Client
	url        string
}

var (
	_ model.Connector = (*Connector)(nil)
	_ model.Embedder  = (*Connector)(nil)
)

// New builds a connector from its validated config. It reads the API key
// once, so a missing secret stops start-up.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (model.Connector, error) {
	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	if _, err := secrets.ResolveText(ctx, deps.Secrets, cfg.APIKeySecret); err != nil {
		return nil, fmt.Errorf("read API key: %w", err)
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	return &Connector{
		name:       name,
		cfg:        cfg,
		secrets:    deps.Secrets,
		httpClient: httpClient,
		url:        strings.TrimRight(cfg.address(), "/") + "/embeddings",
	}, nil
}

// Capabilities reports no capabilities: the connector cannot generate.
func (c *Connector) Capabilities() model.Capabilities {
	return model.Capabilities{}
}

// Generate always fails: the connector only embeds.
func (c *Connector) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, errGenerate)}
}

// ModelID returns the configured model ID, for telemetry labels and the
// price lookup.
func (c *Connector) ModelID() string {
	return c.cfg.Model
}

// Dimensions returns the length of the vectors Embed returns.
func (c *Connector) Dimensions() int {
	return c.cfg.Dimensions
}

// Embed returns one vector for each text, in order. Provider failures are
// returned as a *model.Error. The vectors are as long as the configured
// dimensions, or the call fails with an invalid_request error.
func (c *Connector) Embed(ctx context.Context, texts []string) (model.EmbedResult, error) {
	key, err := secrets.ResolveText(ctx, c.secrets, c.cfg.APIKeySecret)
	if err != nil {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrAuth, Err: fmt.Errorf("connector %s: get credentials: %w", c.name, err)}
	}

	ctx, cancel := context.WithTimeout(ctx, cmpOrDuration(c.cfg.Timeout, defaultTimeout))
	defer cancel()

	ans, requestID, err := c.call(ctx, key, texts)
	if err != nil {
		return model.EmbedResult{}, fmt.Errorf("connector %s: %w", c.name, err)
	}

	vectors, err := c.vectors(ans, len(texts))
	if err != nil {
		return model.EmbedResult{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, err)}
	}

	return model.EmbedResult{
		Vectors:   vectors,
		Usage:     model.Usage{InputTokens: ans.Usage.TotalTokens},
		RequestID: requestID,
	}, nil
}

// request is the body of POST /embeddings.
type request struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	OutputDimension int      `json:"output_dimension"`
}

// answer is the body of a successful POST /embeddings.
type answer struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"usage"`
}

// call sends texts and decodes the answer. It returns a *model.Error for a
// failed call, with the request ID of the answer when it has one.
func (c *Connector) call(ctx context.Context, key string, texts []string) (answer, string, error) {
	body, err := json.Marshal(request{Input: texts, Model: c.cfg.Model, OutputDimension: c.cfg.Dimensions})
	if err != nil {
		return answer{}, "", &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("encode the request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return answer{}, "", &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("build the request: %w", err)}
	}

	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return answer{}, "", classifyTransport(fmt.Errorf("send the request: %w", err))
	}

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(httpclient.LimitBody(resp.Body, maxResponseSize, errResponseTooLarge))
	if err != nil {
		return answer{}, "", classifyTransport(fmt.Errorf("read the answer: %w", err))
	}

	requestID := resp.Header.Get("X-Request-Id")

	if resp.StatusCode != http.StatusOK {
		return answer{}, requestID, classifyStatus(resp, data)
	}

	var ans answer
	if err := json.Unmarshal(data, &ans); err != nil {
		return answer{}, requestID, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("decode the answer: %w", err)}
	}

	return ans, requestID, nil
}

// vectors returns the vectors of ans in the order of the texts, checking
// that there is one for each text and that each has the configured length.
func (c *Connector) vectors(ans answer, want int) ([][]float32, error) {
	if len(ans.Data) != want {
		return nil, fmt.Errorf("%w: sent %d texts, got %d vectors", errBadAnswer, want, len(ans.Data))
	}

	out := make([][]float32, want)

	for _, d := range ans.Data {
		if d.Index < 0 || d.Index >= want || out[d.Index] != nil {
			return nil, fmt.Errorf("%w: vector index %d is out of range or repeated", errBadAnswer, d.Index)
		}

		if len(d.Embedding) != c.cfg.Dimensions {
			return nil, fmt.Errorf("%w: expected %d numbers, got %d; set dimensions to what model %s returns", errWrongVectorLength, c.cfg.Dimensions, len(d.Embedding), c.cfg.Model)
		}

		v := make([]float32, len(d.Embedding))
		for i, f := range d.Embedding {
			v[i] = float32(f)
		}

		out[d.Index] = v
	}

	return out, nil
}

// classifyStatus turns an error response into a *model.Error, with the wait
// the API asked for when it sent one. Voyage's error body is {"detail": ...}.
func classifyStatus(resp *http.Response, body []byte) *model.Error {
	var detail struct {
		Detail json.RawMessage `json:"detail"`
	}

	message := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &detail) == nil && len(detail.Detail) > 0 {
		message = string(detail.Detail)
	}

	if len(message) > maxErrorBody {
		message = message[:maxErrorBody]
	}

	err := fmt.Errorf("%w: status %d: %s", errUnexpectedResponse, resp.StatusCode, message)

	return &model.Error{Kind: kindOfStatus(resp.StatusCode), RetryAfter: model.RetryAfter(resp), Err: err}
}

// kindOfStatus classifies an HTTP status, from Voyage's error codes page.
func kindOfStatus(status int) model.ErrorKind {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return model.ErrInvalid
	case http.StatusUnauthorized, http.StatusForbidden:
		return model.ErrAuth
	case http.StatusTooManyRequests:
		return model.ErrRateLimited
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return model.ErrTimeout
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return model.ErrOverloaded
	default:
		return model.ErrUnknown
	}
}

// classifyTransport turns a failure to send or read into a *model.Error.
func classifyTransport(err error) *model.Error {
	var netErr net.Error

	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &model.Error{Kind: model.ErrTimeout, Err: err}
	}

	return &model.Error{Kind: model.ErrUnknown, Err: err}
}

// cmpOrDuration returns d, or def when d is not above zero.
func cmpOrDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}

	return def
}
