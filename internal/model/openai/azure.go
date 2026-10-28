package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// AzureProvider is the model provider name of Azure OpenAI and Microsoft
// Foundry Models, which this package also registers.
const AzureProvider = "azure"

// azureAPIPath is the path of Azure's v1 API under a resource endpoint. It
// takes the plain OpenAI request shapes, with no api-version.
const azureAPIPath = "/openai/v1"

// azureKeyHeader is the header that carries an Azure API key.
const azureKeyHeader = "api-key"

// entraScope is the OAuth scope of Microsoft Entra ID tokens for Azure
// OpenAI and Foundry Models.
const entraScope = "https://ai.azure.com/.default"

// keyCheckTimeout bounds the call CheckKey makes.
const keyCheckTimeout = 15 * time.Second

// AzureAuth is how an Azure connector signs its calls.
type AzureAuth string

// Azure auth kinds.
const (
	// AzureAuthKey sends the API key in apiKeySecret. It is the default.
	AzureAuthKey AzureAuth = "apiKey"
	// AzureAuthEntra sends a Microsoft Entra ID token, from AKS Workload
	// Identity, a managed identity, the environment or the Azure CLI.
	AzureAuthEntra AzureAuth = "entra"
)

// deploymentPattern matches an Azure deployment name.
var deploymentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// errAzureThinking explains why every thinking mode is rejected.
var errAzureThinking = errors.New("thinking: not accepted by the azure provider; OpenAI-style APIs control reasoning with effort instead (none turns it off where the model supports it)")

// errNoAzureModel explains which models an Azure connector knows.
var errNoAzureModel = errors.New("not an OpenAI model the azure provider knows; describe it in a capabilities block")

func init() {
	registry.Models.Register(AzureProvider, registry.Typed(NewAzure))
}

// AzureConfig is the config section of one Azure OpenAI or Microsoft
// Foundry connector instance. Model is the model the deployment runs, such
// as gpt-6.1-sol: it gives the capabilities and the price.
type AzureConfig struct {
	model.Settings `yaml:",inline"`

	// Endpoint is the resource address, such as
	// https://my-resource.openai.azure.com or
	// https://my-resource.services.ai.azure.com.
	Endpoint string `yaml:"endpoint"`
	// Deployment is the deployment name, sent as the request's model.
	// Empty means the same as Model.
	Deployment string `yaml:"deployment"`
	// API picks the API; empty means responses.
	API API `yaml:"api"`
	// Auth is apiKey (the default) or entra.
	Auth AzureAuth `yaml:"auth"`
	// APIKeySecret holds the API key, required with auth apiKey.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// Declared describes a model the connector does not know. It replaces
	// the known capabilities when set.
	Declared *DeclaredCapabilities `yaml:"capabilities"`
}

var _ model.CapabilityDeclarer = (*AzureConfig)(nil)

// AzureConnector calls one model deployment on Azure OpenAI or Microsoft
// Foundry. It is the openai connector with Azure's endpoint and
// credentials, and it can check its credentials for sidekick doctor.
type AzureConnector struct {
	*Connector

	models openai.ModelService
}

var (
	_ model.Connector  = (*AzureConnector)(nil)
	_ model.KeyChecker = (*AzureConnector)(nil)
)

// Capabilities implements model.CapabilityDeclarer: the declared block when
// there is one, otherwise the known OpenAI model, otherwise nothing.
func (c *AzureConfig) Capabilities() model.Capabilities {
	caps, _ := c.known()

	return caps
}

// Validate checks the section without secrets or network calls: the
// endpoint, deployment, API and auth settings, that the model is known or
// declared, and that the shared settings fit its capabilities. Each error
// starts with the key of its field.
func (c *AzureConfig) Validate() error {
	errs := c.checkEndpoint()

	if c.Thinking != (model.Thinking{}) {
		errs = append(errs, errAzureThinking)
	}

	caps, ok := c.known()

	switch {
	case !ok && c.Model == "":
		errs = append(errs, errors.New("model: required"))

		return errors.Join(errs...)
	case !ok:
		errs = append(errs, fmt.Errorf("model: %q is %w", c.Model, errNoAzureModel))

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

// CheckKey implements model.KeyChecker: it lists the models of the
// resource with the configured credentials, a call that costs nothing, so
// a rejected key or token, a missing role or an unreachable endpoint shows
// without a paid generation.
func (c *AzureConnector) CheckKey(ctx context.Context) error {
	opts, err := c.credentials(ctx)
	if err != nil {
		return &model.Error{Kind: model.ErrAuth, Err: fmt.Errorf("connector %s: get credentials: %w", c.name, err)}
	}

	ctx, cancel := context.WithTimeout(ctx, keyCheckTimeout)
	defer cancel()

	if _, err := c.models.List(ctx, opts...); err != nil {
		return classify(fmt.Errorf("connector %s: list models: %w", c.name, err))
	}

	return nil
}

// deployment returns the deployment name, the model ID when none is set.
func (c *AzureConfig) deployment() string {
	if c.Deployment == "" {
		return c.Model
	}

	return c.Deployment
}

// api returns the API the connector calls.
func (c *AzureConfig) api() API {
	if c.API == "" {
		return APIResponses
	}

	return c.API
}

// baseURL returns the address of the v1 API under the endpoint. An endpoint
// that already ends in /openai/v1 is taken as it is.
func (c *AzureConfig) baseURL() string {
	base := strings.TrimSuffix(c.Endpoint, "/")
	base = strings.TrimSuffix(base, azureAPIPath)

	return base + azureAPIPath + "/"
}

// known returns the capabilities of the configured model, and false when
// the connector knows nothing about it.
func (c *AzureConfig) known() (model.Capabilities, bool) {
	if c.Declared != nil {
		return c.Declared.capabilities(), true
	}

	caps, ok := knownModels[c.Model]

	return caps, ok
}

// checkEndpoint checks the endpoint, deployment, API and auth settings.
// Every call carries credentials, so the endpoint must use https unless
// it is loopback.
func (c *AzureConfig) checkEndpoint() []error {
	var errs []error

	if err := checkAzureEndpoint(c.Endpoint); err != nil {
		errs = append(errs, err)
	}

	if c.Deployment != "" && !deploymentPattern.MatchString(c.Deployment) {
		errs = append(errs, fmt.Errorf("deployment: %q is not a deployment name", c.Deployment))
	}

	if c.API != "" && !slices.Contains(knownAPIs, c.API) {
		errs = append(errs, fmt.Errorf("api: %q is not an API; use responses or chat_completions", c.API))
	}

	keySet := c.APIKeySecret != (registry.SecretRef{})

	switch c.Auth {
	case "", AzureAuthKey:
		if !keySet {
			errs = append(errs, errors.New("apiKeySecret: required unless auth is entra"))
		}
	case AzureAuthEntra:
		if keySet {
			errs = append(errs, errors.New("apiKeySecret: not accepted with auth entra, which uses a Microsoft Entra ID token"))
		}
	default:
		errs = append(errs, fmt.Errorf("auth: %q is not an auth kind; use %s or %s", c.Auth, AzureAuthKey, AzureAuthEntra))
	}

	return errs
}

// NewAzure builds an Azure connector from its validated config. With auth
// apiKey it reads the key once, and with entra it gets a token once, so a
// missing secret or a pod without an Azure identity stops start-up.
func NewAzure(ctx context.Context, name string, cfg AzureConfig, deps registry.Deps) (model.Connector, error) {
	return buildAzure(ctx, name, cfg, deps, defaultAzureCredential)
}

// azureCredentialMaker makes the Entra ID credential of a connector.
type azureCredentialMaker func(client *http.Client) (azcore.TokenCredential, error)

// buildAzure is NewAzure with the way to make an Entra ID credential.
// Tests give one with a fixed token, so they never read the environment or
// call Microsoft.
func buildAzure(ctx context.Context, name string, cfg AzureConfig, deps registry.Deps, makeCredential azureCredentialMaker) (*AzureConnector, error) {
	caps, ok := cfg.known()
	if !ok {
		return nil, fmt.Errorf("model %q: %w", cfg.Model, errUnknownModel)
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	credentials, err := azureCredentials(cfg, deps.Secrets, httpClient, makeCredential)
	if err != nil {
		return nil, fmt.Errorf("set up credentials: %w", err)
	}

	if _, err := credentials(ctx); err != nil {
		return nil, fmt.Errorf("get credentials: %w", err)
	}

	c := &Connector{
		name:                name,
		settings:            cfg.Settings,
		provider:            AzureProvider,
		model:               cfg.deployment(),
		caps:                caps,
		api:                 cfg.api(),
		maxCompletionTokens: true,
		credentials:         credentials,
	}

	c.connect(httpClient, cfg.baseURL())

	return &AzureConnector{
		Connector: c,
		models:    openai.NewModelService(option.WithHTTPClient(httpClient), option.WithBaseURL(cfg.baseURL()), option.WithMaxRetries(0)),
	}, nil
}

// azureCredentials returns the credentials function of cfg: the api-key
// header from the Secret, or a bearer token from Entra ID.
func azureCredentials(cfg AzureConfig, s registry.Secrets, client *http.Client, makeCredential azureCredentialMaker) (func(ctx context.Context) ([]option.RequestOption, error), error) {
	if cfg.Auth != AzureAuthEntra {
		if s == nil {
			return nil, errNoSecrets
		}

		return func(ctx context.Context) ([]option.RequestOption, error) {
			key, err := secrets.ResolveText(ctx, s, cfg.APIKeySecret)
			if err != nil {
				return nil, fmt.Errorf("resolve %s/%s: %w", cfg.APIKeySecret.Name, cfg.APIKeySecret.Key, err)
			}

			return []option.RequestOption{option.WithHeader(azureKeyHeader, key)}, nil
		}, nil
	}

	cred, err := makeCredential(client)
	if err != nil {
		return nil, fmt.Errorf("make Entra ID credential: %w", err)
	}

	return func(ctx context.Context) ([]option.RequestOption, error) {
		tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{entraScope}})
		if err != nil {
			return nil, fmt.Errorf("get Entra ID token: %w", err)
		}

		return []option.RequestOption{option.WithAPIKey(tok.Token)}, nil
	}, nil
}

// defaultAzureCredential makes the default Azure credential chain: the
// environment, AKS Workload Identity, a managed identity, then the Azure
// CLI and the Azure Developer CLI for local runs. Its tokens are cached
// and refreshed before they expire.
func defaultAzureCredential(client *http.Client) (azcore.TokenCredential, error) {
	cred, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
		ClientOptions: azcore.ClientOptions{Transport: client},
	})
	if err != nil {
		return nil, fmt.Errorf("default Azure credential: %w", err)
	}

	return cred, nil
}

// checkAzureEndpoint checks that raw is an absolute https URL with no path
// beyond /openai/v1. Plain http is allowed only to a loopback host, such as
// a local proxy, since every call carries credentials.
func checkAzureEndpoint(raw string) error {
	if raw == "" {
		return errors.New("endpoint: required, for example https://my-resource.openai.azure.com")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("endpoint: %w", err)
	}

	switch path := strings.TrimSuffix(u.Path, "/"); {
	case u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return fmt.Errorf("endpoint: %q must be an absolute https URL", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("endpoint: %q must use https; plain http is allowed only for localhost, 127.0.0.0/8 or ::1", raw)
	case path != "" && path != azureAPIPath:
		return fmt.Errorf("endpoint: %q must be the resource address, without a path such as /openai/deployments", raw)
	case u.RawQuery != "":
		return fmt.Errorf("endpoint: %q must not have a query, such as api-version; the v1 API needs none", raw)
	}

	return nil
}
