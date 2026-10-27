// Package google is the model connector for Gemini models on the Gemini API
// and on Vertex AI (now named Gemini Enterprise Agent Platform), through
// Google's Gen AI SDK. It registers itself as the model provider "google".
//
// The backend setting picks the service: gemini calls the Gemini API with
// an API key, and vertex calls Vertex AI with Application Default
// Credentials, which cover GKE Workload Identity. Both use the
// generateContent API, streamed and collected into one model.Response. The
// SDK's own retries and automatic continuation are off, since
// model.Generate retries for every connector, and every setting the SDK
// would otherwise read from the environment is set explicitly.
package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"cloud.google.com/go/auth/credentials"
	"google.golang.org/genai"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Provider is the model provider name this package registers.
const Provider = "google"

// defaultTimeout bounds one call when the timeout setting is zero, as in
// the other connectors. Long thinking can take minutes.
const defaultTimeout = 10 * time.Minute

// keyCheckTimeout bounds the call CheckKey makes.
const keyCheckTimeout = 15 * time.Second

// cloudPlatformScope is the OAuth scope Vertex AI calls need.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// Default addresses of the two backends.
const (
	geminiBaseURL       = "https://generativelanguage.googleapis.com/"
	vertexGlobalBaseURL = "https://aiplatform.googleapis.com/"
)

// multiRegions are the Vertex AI multi-region locations, served from their
// own regional endpoint.
var multiRegions = map[string]bool{"us": true, "eu": true}

// errUnknownModel is returned by New for a model that is neither known nor
// declared. Validate reports the same problem with more detail.
var errUnknownModel = errors.New("not a known model and no capabilities block")

// errNoSecrets is returned when a gemini connector is built without a way
// to read its API key.
var errNoSecrets = errors.New("no secrets resolver in deps")

func init() {
	registry.Models.Register(Provider, registry.Typed(New))
}

// Connector calls one configured Gemini model.
type Connector struct {
	name   string
	cfg    Config
	model  knownModel
	models *genai.Models
}

var (
	_ model.Connector  = (*Connector)(nil)
	_ model.KeyChecker = (*Connector)(nil)
)

// Capabilities reports what the configured model supports.
func (c *Connector) Capabilities() model.Capabilities {
	return c.model.caps
}

// ModelID returns the configured model ID, for telemetry labels and the
// price lookup.
func (c *Connector) ModelID() string {
	return c.cfg.Model
}

// Generate runs one model turn. Provider failures are returned as a
// *model.Error; a request the model cannot take is an invalid_request
// error before any network call.
func (c *Connector) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	contents, config, err := c.request(req)
	if err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, err)}
	}

	ctx, cancel := context.WithTimeout(ctx, cmpOrDuration(c.cfg.Timeout, defaultTimeout))
	defer cancel()

	start := time.Now()

	var acc accumulator

	for chunk, err := range c.models.GenerateContentStream(ctx, c.cfg.Model, contents, config) {
		if err != nil {
			return model.Response{}, classify(fmt.Errorf("connector %s: generate content: %w", c.name, err))
		}

		acc.add(chunk)
	}

	resp, err := acc.response()
	if err != nil {
		return model.Response{}, classify(fmt.Errorf("connector %s: %w", c.name, err))
	}

	resp.Model = c.cfg.Model
	resp.Latency = time.Since(start)

	return resp, nil
}

// CheckKey implements model.KeyChecker: it lists one model with the
// configured credentials, a call that costs nothing, so a rejected key, a
// missing permission or an unreachable API shows without a paid generation.
func (c *Connector) CheckKey(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, keyCheckTimeout)
	defer cancel()

	if _, err := c.models.List(ctx, &genai.ListModelsConfig{PageSize: 1}); err != nil {
		return classify(fmt.Errorf("connector %s: list models: %w", c.name, err))
	}

	return nil
}

// New builds a connector from its validated config. With backend gemini it
// reads the API key once, so a missing secret stops start-up; with vertex
// it finds the Application Default Credentials, so a pod without a Google
// identity stops start-up too.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (model.Connector, error) {
	return build(ctx, name, cfg, deps, findCredentials)
}

// credentialFinder finds the Vertex AI credentials and their quota project.
type credentialFinder func(ctx context.Context, client *http.Client) (tokenSource, string, error)

// build is New with the way to find Vertex AI credentials. Tests give one
// with a fixed token, so they never read the environment or call Google.
func build(ctx context.Context, name string, cfg Config, deps registry.Deps, find credentialFinder) (*Connector, error) {
	m, ok := cfg.known()
	if !ok {
		return nil, fmt.Errorf("model %q: %w", cfg.Model, errUnknownModel)
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	transport := &authTransport{next: transportOf(httpClient)}

	cc := &genai.ClientConfig{
		HTTPClient: &http.Client{Transport: transport, Timeout: httpClient.Timeout},
		HTTPOptions: genai.HTTPOptions{
			BaseURL:      cfg.baseURL(),
			RetryOptions: &genai.HTTPRetryOptions{Attempts: genai.Ptr[int32](1)},
		},
	}

	switch cfg.backend() {
	case BackendVertex:
		tokens, quotaProject, err := find(ctx, httpClient)
		if err != nil {
			return nil, fmt.Errorf("find Google credentials: %w", err)
		}

		transport.tokens, transport.quotaProject = tokens, quotaProject
		cc.Backend, cc.Project, cc.Location = genai.BackendVertexAI, cfg.Project, cfg.location()
	default:
		if deps.Secrets == nil {
			return nil, errNoSecrets
		}

		key, err := secrets.ResolveText(ctx, deps.Secrets, cfg.APIKeySecret)
		if err != nil {
			return nil, fmt.Errorf("read API key: %w", err)
		}

		// The SDK needs a key to build a Gemini API client; the transport
		// replaces it with the current one on every request.
		transport.secrets, transport.apiKey = deps.Secrets, cfg.APIKeySecret
		cc.Backend, cc.APIKey = genai.BackendGeminiAPI, key
	}

	client, err := genai.NewClient(ctx, cc)
	if err != nil {
		return nil, fmt.Errorf("create Gen AI client: %w", err)
	}

	return &Connector{name: name, cfg: cfg, model: m, models: client.Models}, nil
}

// findCredentials finds the Application Default Credentials: GKE Workload
// Identity or another metadata server, a credentials file named by
// GOOGLE_APPLICATION_CREDENTIALS (including workload identity federation
// from another cloud), or the gcloud user credentials.
func findCredentials(ctx context.Context, client *http.Client) (tokenSource, string, error) {
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{cloudPlatformScope},
		Client: client,
	})
	if err != nil {
		return nil, "", fmt.Errorf("detect default credentials: %w", err)
	}

	quotaProject, err := creds.QuotaProjectID(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("read quota project: %w", err)
	}

	return creds, quotaProject, nil
}

// transportOf returns the round tripper of client, or the default one.
func transportOf(client *http.Client) http.RoundTripper {
	if client.Transport != nil {
		return client.Transport
	}

	return http.DefaultTransport
}

// cmpOrDuration returns d, or def when d is not above zero.
func cmpOrDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}

	return def
}
