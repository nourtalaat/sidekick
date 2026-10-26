# Writing a model connector

A model connector lets Sidekick talk to one kind of model provider. The
investigator only ever sees the `model.Connector` interface in
`internal/model`, so everything that is special about a provider stays
inside its connector package.

This guide walks through the steps for a new connector. The example in
[`examples/model-echo`](../../examples/model-echo/echo.go) follows the same
steps, and its comments carry the step numbers used here. It answers each
turn with the last user message, so you can read the shape of a connector
without a real API in the way. The example is built and tested in CI, but
nothing imports it, so it is not part of the `sidekick` binary. The
Anthropic connector in `internal/model/anthropic` is the full version of
every step.

## The interface

```go
type Connector interface {
	Capabilities() Capabilities
	Generate(ctx context.Context, req Request) (Response, error)
}
```

`Generate` runs one model turn: it gets the whole conversation so far
(system text, messages, tools, tool choice and the shared settings) and
returns the model's message, why it stopped, and how many tokens it used.

## Step 1: the config struct

Each connector instance is one entry under `model.connectors` in
`sidekick.yaml`. The `provider` key picks the connector; the other keys
belong to the connector's own config struct.

- Embed `model.Settings` with the `yaml:",inline"` tag. That gives every
  connector the same `model`, `maxTokens`, `timeout`, `retries`, `thinking`
  and `effort` keys.
- Add the provider's own keys next to it, such as a base URL or a region.
- Never put a secret in the struct as a string. Use a `registry.SecretRef`
  (`{ name, key }`), which points at a file mounted from a Kubernetes
  Secret. Config validation finds every `SecretRef` in your struct by
  itself, wherever it is.

Unknown keys are errors, so a typo in a user's config is reported instead of
being ignored.

## Step 2: capabilities and validation

Users must find mistakes with `sidekick config validate`, which runs
without secrets and without network calls. Two methods on the config struct
make that work:

- `Capabilities() model.Capabilities`, which makes the struct a
  `model.CapabilityDeclarer`. Return what the configured model can do: tool
  calling, forced tool choice, thinking modes, effort levels, context size
  and maximum output. Config validation then checks the shared settings
  against it, and checks that the model given the `investigate` role can
  call tools.
- `Validate() error`, for rules of your own. Start each error with the key
  of its field and a colon, for example `baseURL: must be an absolute URL`,
  so the error is shown next to the right line of the config. Join several
  problems with `errors.Join` so the user sees them all at once.

Keep a table of the model IDs your connector knows, with their
capabilities. For a model that is not in the table, accept a `capabilities:`
block in config and fail validation when it is missing. Reject shared
setting values your provider does not have; for example, the Anthropic
connector rejects the `none` and `minimal` effort levels, which only OpenAI
has.

## Step 3: the constructor

Write a constructor with this shape:

```go
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (model.Connector, error)
```

It gets the config already decoded and validated. `deps` holds the shared
services:

- `deps.Secrets` reads secret references. Use `secrets.ResolveText` for API
  keys, which drops the trailing newline a Secret file often has. Reading
  the key once in the constructor makes a missing secret stop start-up;
  reading it again on every call picks up a rotated key without a restart.
- `deps.HTTP` is the HTTP client to send requests with. Tests replace it to
  serve recorded responses.
- `deps.Logger` is the structured logger.
- `deps.Metrics` is where the connector registers its own Prometheus
  metrics, through `registry.RegisterOrExisting`, so several instances of
  the type share one collector. It is nil in tests that do not set it.

If the provider's SDK retries on its own, turn that off (`retries` is
handled for every connector at once, see step 5). Also make sure the SDK
does not pick up credentials or settings from environment variables you did
not ask for.

## Step 4: one model turn

Map `model.Request` to the provider's request, send it, and map the answer
back.

- **Message parts.** `model.Part` is a closed set: `Text`, `ToolCall`,
  `ToolResult` and `Reasoning`. A type switch over them covers every case.
- **Settings.** Use the request's `MaxTokens`, `Thinking` and `Effort` when
  they are set, and the config's otherwise. Check them against the model's
  capabilities before sending anything, and return an `invalid_request`
  error for a request the model cannot take.
- **Reasoning.** Some providers return thinking data that must be sent back
  unchanged in later turns. Keep it in a `model.Reasoning` part with
  `Provider` set to your provider name, and send its text, signature or
  opaque data back exactly as you got them. Drop `Reasoning` parts from any
  other provider, since a conversation can move between connectors.
- **Forced tool choice.** A connector whose provider cannot force a tool
  while the model thinks sends tool choice `auto` instead, so callers must
  not depend on a tool being forced.
- **Prompt caching.** `Text.Cache` and `ToolSpec.Cache` mark cache
  breakpoints. Map them to the provider's cache markers, or ignore them if
  the provider caches on its own.
- **Long outputs.** If a call can run for minutes, stream it inside the
  connector so it does not hit HTTP timeouts, and still return one
  `Response`.
- **Usage, not cost.** Fill in `Response.Usage` with input, output, cache
  read and cache write tokens. Do not compute cost in the connector: the
  caller prices every call with the shared price table in
  `internal/model/pricing`, so cost is worked out in one place.
- **Stop reason.** Map the provider's reasons to `end_turn`, `tool_use`,
  `max_tokens`, `refusal` or `other`.

## Step 5: errors

Return every provider failure as a `*model.Error` with one of these kinds:

| Kind | When | Retried |
|---|---|---|
| `rate_limited` | Too many requests or tokens | Yes |
| `overloaded` | The service is busy or had a passing server error | Yes |
| `timeout` | The call or the connection timed out | Yes |
| `invalid_request` | The request is wrong, or the model cannot take it | No |
| `context_length` | The prompt does not fit the context window | No |
| `auth` | A bad key, missing permission or an account problem | No |
| `unknown` | Anything else | No |

When the provider says how long to wait (a `retry-after` header), put it in
`RetryAfter`. `model.Generate` retries the retryable kinds up to the
`retries` setting, waits for `RetryAfter` when it is set, and otherwise
backs off exponentially with jitter. Connectors do not retry by themselves.

## Step 6: telemetry and prices

There is little to do here. The app wraps every connector it builds with
`instrument.Wrap`, which records the `model.generate` trace span and the
model metrics for each call. Two things help it:

- Implement `ModelID() string` on the connector, returning the configured
  model ID. The wrapper uses it for metric labels and to look up the price.
- Add the models your connector knows to the shipped price table in
  `internal/model/pricing/shipped.go`, in US dollars per million tokens, and
  update the date the prices were checked. Users can override any price
  with `model.prices` in config.

Never put prompt or response content in a log field, span attribute or
metric label.

## Step 7: registration

Register the provider in the package's `init` function:

```go
func init() {
	registry.Models.Register("myprovider", registry.Typed(New))
}
```

`registry.Typed` decodes the config section into your struct, runs its
`Validate` method, and calls your constructor. Then add a blank import of
your package to `internal/plugins/plugins.go`, which is what links built-in
plugins into the binary.

## Step 8: tests

- Test against recorded provider responses served by `internal/fakehttp`,
  never the real API, and never with a real key. Cover at least a text
  turn, a tool call, reasoning, a refusal, the output limit, a rate limit
  with `retry-after`, an overload and a bad key.
- Start one `fakehttp` server per test or subtest and point the connector
  at it through its base URL and the server's client, so parallel tests
  share nothing.
- Show with a two-turn test that reasoning from the first turn reaches the
  provider unchanged in the second.
- Table-test `Validate` against the capabilities: unsupported effort levels
  and thinking modes, budgets, and an unknown model without a
  `capabilities:` block.

## Step 9: user docs

Describe the provider's keys, its known models and its limits in
[`docs/configuration.md`](../configuration.md), next to the other providers.
