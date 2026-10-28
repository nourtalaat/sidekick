//go:generate mockgen -source model.go -destination mocks_model.go -package model -exclude_interfaces Part -write_package_comment=false

// Package model defines the provider-neutral interface to LLM providers.
// Provider differences stay inside each connector package.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Connector is one configured model instance, such as "claude" or "cheap".
type Connector interface {
	// Capabilities reports what the configured model supports.
	Capabilities() Capabilities
	// Generate runs one model turn. It returns a *Error for provider failures.
	Generate(ctx context.Context, req Request) (Response, error)
}

// Embedder is implemented by connectors that can embed text (optional vector memory).
type Embedder interface {
	// Embed returns one vector for each text, in the order of texts. It
	// returns a *Error for provider failures, and an invalid_request error
	// for a vector whose length is not Dimensions.
	Embed(ctx context.Context, texts []string) (EmbedResult, error)
	// Dimensions is the length of every vector Embed returns.
	Dimensions() int
}

// EmbedResult is the answer to one Embed call.
type EmbedResult struct {
	// Vectors holds one vector for each input text, in the order of the inputs.
	Vectors [][]float32
	// Usage counts the input tokens the call used, when the provider says.
	Usage Usage
	// RequestID is the provider's ID of the call, for support requests.
	RequestID string
}

// EmbeddingConfigured is implemented by a connector's config struct that
// can describe an embedding model, the way CapabilityDeclarer describes a
// generation model. Config validation uses it to keep embedding settings
// out of generation connectors and generation models out of
// memory.vector.embeddings.
type EmbeddingConfigured interface {
	// Embedding reports whether the config names an embedding model, and
	// the dimensions it sets, which is zero when it sets none.
	Embedding() (dimensions int, ok bool)
}

// Role is the job a connector does for Sidekick.
type Role string

// Roles.
const (
	RoleTriage      Role = "triage"
	RoleInvestigate Role = "investigate"
	RoleJudge       Role = "judge"
	RoleSummarize   Role = "summarize"
	// RoleEmbed labels embedding calls in telemetry. It is not a role to
	// configure under model.roles.
	RoleEmbed Role = "embed"
)

// Effort is the shared effort setting. Connectors map it to provider values.
type Effort string

// Effort levels. EffortDefault leaves the provider default in place.
const (
	EffortDefault Effort = ""
	EffortNone    Effort = "none"
	EffortMinimal Effort = "minimal"
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

// ThinkingMode is the shared thinking setting.
type ThinkingMode string

// Thinking modes.
const (
	ThinkingOff      ThinkingMode = "off"
	ThinkingAdaptive ThinkingMode = "adaptive"
	ThinkingEnabled  ThinkingMode = "enabled"
)

// Thinking configures extended reasoning. BudgetTokens applies to ThinkingEnabled only.
type Thinking struct {
	Mode         ThinkingMode `yaml:"mode"`
	BudgetTokens int          `yaml:"budgetTokens"`
}

// Capabilities describes a configured model.
type Capabilities struct {
	ToolCalling bool
	// ToolChoice is false when the endpoint ignores a forced tool choice (for example Ollama).
	ToolChoice       bool
	ThinkingModes    []ThinkingMode
	EffortLevels     []Effort
	StructuredOutput bool
	PromptCaching    bool
	ContextTokens    int
	MaxOutputTokens  int
}

// SupportsThinking reports whether mode is accepted. An empty mode, which is
// what a config without a thinking section decodes to, means ThinkingOff.
func (c Capabilities) SupportsThinking(mode ThinkingMode) bool {
	if mode == "" || mode == ThinkingOff {
		return true
	}

	return slices.Contains(c.ThinkingModes, mode)
}

// SupportsEffort reports whether level is accepted.
func (c Capabilities) SupportsEffort(level Effort) bool {
	if level == EffortDefault {
		return true
	}

	return slices.Contains(c.EffortLevels, level)
}

// CapabilityDeclarer is implemented by a connector's config struct when it
// knows the model's capabilities before the connector is built, from the
// connector's table of known models or a capabilities block in config.
// Config validation then checks the shared settings and the investigate role
// without building the connector, so `sidekick config validate` catches
// those mistakes without secrets or network calls.
type CapabilityDeclarer interface {
	Capabilities() Capabilities
}

// MessageRole is who sent a message in the conversation.
type MessageRole string

// Message roles. Tool results travel in user messages, as most providers expect.
const (
	User      MessageRole = "user"
	Assistant MessageRole = "assistant"
)

// Message is one conversation turn.
type Message struct {
	Role  MessageRole
	Parts []Part
}

// Part is one piece of a message. The unexported method seals the set of part types.
type Part interface {
	isPart()
}

// Text is plain text. Cache marks a prompt caching breakpoint after this part.
type Text struct {
	Text  string
	Cache bool
}

// ToolCall is the model asking to run a tool.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers one ToolCall. Content is already redacted and wrapped as untrusted data.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Reasoning is provider-specific thinking output. It must be sent back unchanged
// in later turns to the same provider, and is dropped for any other provider.
type Reasoning struct {
	// Provider is the connector type that produced it, for example "anthropic".
	Provider string
	// ID is a provider item ID, for example an OpenAI reasoning item.
	ID string
	// Text is readable reasoning, when the provider returns it.
	Text string
	// Signature or encrypted content that the provider checks when it is passed back.
	Signature string
	// Redacted holds opaque reasoning the provider returned without text.
	Redacted []byte
}

func (Text) isPart()       {}
func (ToolCall) isPart()   {}
func (ToolResult) isPart() {}
func (Reasoning) isPart()  {}

// ToolSpec describes a tool the model may call.
type ToolSpec struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object.
	InputSchema json.RawMessage
	Cache       bool
}

// ToolChoiceMode controls whether and which tool the model must call.
type ToolChoiceMode string

// Tool choice modes.
const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceAny      ToolChoiceMode = "any"
	ToolChoiceRequired ToolChoiceMode = "tool"
	ToolChoiceNone     ToolChoiceMode = "none"
)

// ToolChoice selects a tool choice mode. Name is set for ToolChoiceRequired.
type ToolChoice struct {
	Mode ToolChoiceMode
	Name string
}

// Request is one model turn.
type Request struct {
	System      []Text
	Messages    []Message
	Tools       []ToolSpec
	ToolChoice  ToolChoice
	MaxTokens   int
	Temperature *float64
	Thinking    Thinking
	Effort      Effort
	// Metadata is sent to providers that accept request tags, and to traces.
	Metadata map[string]string
}

// Request metadata keys that Sidekick sets on every call, so telemetry can
// label a call with the role it serves and the route of its investigation.
const (
	MetadataRole  = "sidekick.role"
	MetadataRoute = "sidekick.route"
)

// StopReason says why the model stopped.
type StopReason string

// Stop reasons.
const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopOther     StopReason = "other"
)

// Usage counts tokens for one call.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
}

// Add returns the sum of two usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + o.InputTokens,
		OutputTokens:     u.OutputTokens + o.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + o.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + o.CacheWriteTokens,
		ReasoningTokens:  u.ReasoningTokens + o.ReasoningTokens,
	}
}

// PromptTokens returns the length of the prompt: input tokens plus the
// tokens read from and written to the prompt cache.
func (u Usage) PromptTokens() int64 {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// Response is the result of one model turn.
type Response struct {
	Message    Message
	StopReason StopReason
	Usage      Usage
	Model      string
	RequestID  string
	Latency    time.Duration
}

// ErrorKind classifies provider failures so the caller can decide to retry.
type ErrorKind string

// Error kinds.
const (
	ErrRateLimited   ErrorKind = "rate_limited"
	ErrOverloaded    ErrorKind = "overloaded"
	ErrInvalid       ErrorKind = "invalid_request"
	ErrAuth          ErrorKind = "auth"
	ErrContextLength ErrorKind = "context_length"
	ErrTimeout       ErrorKind = "timeout"
	ErrUnknown       ErrorKind = "unknown"
)

// Error is a classified provider failure.
type Error struct {
	Kind       ErrorKind
	RetryAfter time.Duration
	Err        error
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("model %s: %v", e.Kind, e.Err)
}

// Unwrap returns the provider error.
func (e *Error) Unwrap() error {
	return e.Err
}

// Retryable reports whether the call may succeed if repeated.
func (e *Error) Retryable() bool {
	switch e.Kind {
	case ErrRateLimited, ErrOverloaded, ErrTimeout:
		return true
	default:
		return false
	}
}

// tokensPerPrice is the number of tokens a Price field is quoted for.
const tokensPerPrice = 1_000_000

// Price is the cost per million tokens in micro-dollars (1 USD = 1,000,000).
//
// Some models cost more per token when the prompt is long. For them, Above
// holds the price of a call whose prompt is longer than TierAboveInputTokens
// tokens. Each call is priced on its own, at one price for all its tokens.
type Price struct {
	InputPerMTok      int64
	OutputPerMTok     int64
	CacheReadPerMTok  int64
	CacheWritePerMTok int64

	// TierAboveInputTokens is the prompt length, in tokens, above which Above
	// applies. The prompt length counts input, cache read and cache write
	// tokens (Usage.PromptTokens), which is how Anthropic bills its tiers.
	TierAboveInputTokens int64
	// Above is the price of a call with a longer prompt. Nil means one price
	// for every prompt length.
	Above *Price
}

// Cost returns the cost of usage in micro-dollars. Reasoning tokens are billed as output.
func (p Price) Cost(u Usage) int64 {
	if p.Above != nil && u.PromptTokens() > p.TierAboveInputTokens {
		return p.Above.Cost(u)
	}

	total := u.InputTokens*p.InputPerMTok +
		u.OutputTokens*p.OutputPerMTok +
		u.CacheReadTokens*p.CacheReadPerMTok +
		u.CacheWriteTokens*p.CacheWritePerMTok

	return total / tokensPerPrice
}

// ErrNoToolCalling is returned when a model without tool calling is given
// the investigate or triage role, which both end by calling a tool.
var ErrNoToolCalling = errors.New("model cannot call tools")

// CheckRole validates that a connector with caps can fill role.
func CheckRole(role Role, caps Capabilities) error {
	if (role == RoleInvestigate || role == RoleTriage) && !caps.ToolCalling {
		return fmt.Errorf("role %s: %w", role, ErrNoToolCalling)
	}

	return nil
}
