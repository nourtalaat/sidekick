// Package bedrock is the model connector for Amazon Bedrock, on the
// Converse API. It registers itself as the model provider "bedrock".
//
// A call with a large output limit uses ConverseStream, so long outputs and
// long thinking do not hit HTTP timeouts; a smaller one uses Converse. Both
// return one model.Response. Credentials come from the default AWS chain,
// which covers IRSA and EKS Pod Identity. The connector reports token usage
// only; callers price it with the pricing package. The SDK's own retries
// are off, since model.Generate retries for every connector.
//
// A connector whose model is an embedding model, Amazon Titan Text
// Embeddings V2 or Cohere Embed v4, or that sets dimensions, embeds text
// with InvokeModel instead of generating: it implements model.Embedder and
// is valid only as memory.vector.embeddings.
package bedrock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/nourtalaat/sidekick/internal/awsauth"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Provider is the model provider name this package registers.
const Provider = "bedrock"

// defaultTimeout bounds one call when the timeout setting is zero, as in
// the anthropic connector.
const defaultTimeout = 10 * time.Minute

// streamAbove is the output limit above which a call streams. Anthropic
// requires streaming for Claude calls with more than 21,333 output tokens,
// because they can run past HTTP timeouts.
const streamAbove = 21_333

// errUnknownModel is returned by New for a model that is neither known nor
// declared. Validate reports the same problem with more detail.
var errUnknownModel = errors.New("not a known model and no capabilities block")

// errIncomplete is returned when the stream ended before the message did.
var errIncomplete = errors.New("stream ended before the message was complete")

func init() {
	registry.Models.Register(Provider, registry.Typed(New))
}

// Connector calls one configured Bedrock model.
type Connector struct {
	name        string
	cfg         Config
	model       knownModel
	anthropic   bool
	client      *bedrockruntime.Client
	credentials aws.CredentialsProvider
	logger      *slog.Logger
	// embedding is the embedding model of an embedding connector, and nil
	// for a generation connector.
	embedding *embeddingModel
	// dimensions is the length of the vectors of an embedding connector.
	dimensions int
}

var (
	_ model.Connector = (*Connector)(nil)
	_ model.Embedder  = (*Connector)(nil)
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
	if c.embedding != nil {
		return model.Response{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, errEmbeddingOnly)}
	}

	in, err := c.input(req)
	if err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrInvalid, Err: fmt.Errorf("connector %s: %w", c.name, err)}
	}

	ctx, cancel := context.WithTimeout(ctx, cmpOrDuration(c.cfg.Timeout, defaultTimeout))
	defer cancel()

	if _, err := c.credentials.Retrieve(ctx); err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrAuth, Err: fmt.Errorf("connector %s: get AWS credentials: %w", c.name, err)}
	}

	start := time.Now()

	var t turn
	if in.stream() {
		t, err = c.converseStream(ctx, in)
	} else {
		t, err = c.converse(ctx, in)
	}

	if err != nil {
		return model.Response{}, classify(fmt.Errorf("connector %s: %w", c.name, err))
	}

	resp := response(t)
	resp.Model = c.cfg.Model
	resp.Latency = time.Since(start)

	return resp, nil
}

// converse sends one Converse request and collects its answer.
func (c *Connector) converse(ctx context.Context, in converseInput) (turn, error) {
	out, err := c.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId:                      aws.String(c.cfg.Model),
		Messages:                     in.messages,
		System:                       in.system,
		ToolConfig:                   in.tools,
		InferenceConfig:              in.inference,
		AdditionalModelRequestFields: in.additional,
	})
	if err != nil {
		return turn{}, fmt.Errorf("converse: %w", err)
	}

	t := turn{stop: out.StopReason, usage: out.Usage}
	t.requestID, _ = awsmiddleware.GetRequestIDMetadata(out.ResultMetadata)

	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return t, errIncomplete
	}

	t.blocks, err = blocksOf(msg.Value.Content)
	if err != nil {
		return t, err
	}

	return t, nil
}

// converseStream sends one ConverseStream request and collects the events
// into one turn.
func (c *Connector) converseStream(ctx context.Context, in converseInput) (t turn, err error) {
	out, err := c.client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId:                      aws.String(c.cfg.Model),
		Messages:                     in.messages,
		System:                       in.system,
		ToolConfig:                   in.tools,
		InferenceConfig:              in.inference,
		AdditionalModelRequestFields: in.additional,
	})
	if err != nil {
		return t, fmt.Errorf("converse stream: %w", err)
	}

	t.requestID, _ = awsmiddleware.GetRequestIDMetadata(out.ResultMetadata)

	stream := out.GetStream()

	defer func() {
		if cerr := stream.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close stream: %w", cerr)
		}
	}()

	var acc accumulator
	for event := range stream.Events() {
		acc.add(event)
	}

	if serr := stream.Err(); serr != nil {
		return t, fmt.Errorf("read stream: %w", serr)
	}

	if acc.stop == "" {
		return t, errIncomplete
	}

	t.blocks, t.stop, t.usage = acc.blocks(), acc.stop, acc.usage

	return t, nil
}

// New builds a connector from its validated config. Credentials come from
// the default AWS chain and are read again whenever they expire, so rotated
// credentials are used without a restart.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (model.Connector, error) {
	return build(ctx, name, cfg, deps, config.LoadDefaultConfig)
}

// build is New with the AWS config loader to use. Tests give one with fixed
// credentials, so they never read the environment or call AWS.
func build(ctx context.Context, name string, cfg Config, deps registry.Deps, load awsauth.Loader) (*Connector, error) {
	var (
		m         knownModel
		embedding *embeddingModel
	)

	if _, isEmbedding := cfg.Embedding(); isEmbedding {
		e, ok := lookupEmbeddingModel(cfg.Model)
		if !ok {
			return nil, fmt.Errorf("model %q: %w", cfg.Model, errUnknownModel)
		}

		embedding = &e
	} else {
		var ok bool

		m, ok = cfg.known()
		if !ok {
			return nil, fmt.Errorf("model %q: %w", cfg.Model, errUnknownModel)
		}
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	awsCfg, err := awsauth.Load(ctx, load, cfg.Region, httpClient)
	if err != nil {
		return nil, fmt.Errorf("set up AWS credentials: %w", err)
	}

	client := bedrockruntime.NewFromConfig(awsCfg, func(o *bedrockruntime.Options) {
		o.Retryer = aws.NopRetryer{}
		o.RetryMaxAttempts = 0
		// Only the AWS credential chain signs requests: a Bedrock API key
		// in AWS_BEARER_TOKEN_BEDROCK is not used, and the endpoint comes
		// from config only.
		o.BearerAuthTokenProvider = nil
		o.AuthSchemePreference = nil
		o.BaseEndpoint = nil

		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})

	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &Connector{
		name:        name,
		cfg:         cfg,
		model:       m,
		anthropic:   isAnthropic(cfg.Model),
		client:      client,
		credentials: awsCfg.Credentials,
		logger:      logger,
		embedding:   embedding,
		dimensions:  cfg.Dimensions,
	}, nil
}

// cmpOrDuration returns d, or def when d is not above zero.
func cmpOrDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}

	return def
}
