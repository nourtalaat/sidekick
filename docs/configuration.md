# Configuration

Sidekick reads one YAML file, `sidekick.yaml`. With the Helm chart it is
rendered from the chart's `config` value. This page describes the sections
the first release supports, the checks Sidekick runs on the file, and how to
run those checks yourself with `sidekick config validate`.

## How the file is read

- **Unknown keys are errors.** A misspelled key, or a section that this
  version does not support yet, stops Sidekick from starting instead of being
  ignored. The error says which key and, when a known key is close, suggests
  it.
- **Every problem is reported at once,** sorted by the path of the field it
  is about, for example `routes[0].outputs[1]`.
- **Secrets are never written in the file.** Each one is a reference to a
  key in a Kubernetes Secret, written as `{ name: <secret>, key: <key> }`.
  Sidekick reads the value from the file `<secrets.dir>/<name>/<key>`, which
  is where the Helm chart mounts Secrets.
- **Durations** are written like `30s`, `10m` or `1h30m`.
- YAML anchors, aliases and merge keys (`<<`) work as usual.

## Sections

### `server`

```yaml
server:
  publicURL: https://sidekick.example.com   # used for links; empty turns links off
  listen: ":8080"                           # API, UI and webhooks
  metricsListen: ":9090"                    # /metrics, /healthz and /readyz
```

### `database`

```yaml
database:
  urlSecret: { name: sidekick-db, key: url }   # required: the Postgres URL
  maxConns: 20                                # size of the connection pool
  migrations: startup                         # startup or manual
```

`migrations` says when the database schema is migrated:

- `startup` (the default): each replica migrates when it starts. When
  several start at once, one migrates while the others wait for it, so no
  extra step is needed for an upgrade.
- `manual`: Sidekick never migrates. Run `sidekick migrate up` yourself
  before the new release starts serving. Until the schema is current, the
  replica stays up but `/readyz` fails with a message that says so, and the
  replica gets no traffic. Use this where production migrations are run by
  hand.

The Helm chart also offers a `job` mode (`migrations.mode: job`), which
migrates in a Job before each install and upgrade and runs Sidekick in
`manual` mode.

### `secrets`

```yaml
secrets:
  dir: /var/run/secrets/sidekick   # where secret files are mounted
```

Point `dir` at `/run/secrets` to use Docker or Podman secrets.

### `model`

Model connectors are named instances. Roles say which connector does which
job, and `investigate` must be set. Prices override the built-in price table,
in US dollars per million tokens.

```yaml
model:
  connectors:
    claude:
      provider: anthropic
      model: claude-sonnet-5-5
      maxTokens: 16000
      thinking: { mode: adaptive }
      effort: high
      timeout: 120s
      retries: 3
      apiKeySecret: { name: sidekick-anthropic, key: apiKey }
  roles:
    investigate: claude      # required, and the model must be able to call tools
    triage: claude           # optional; see "Triage" in investigations.md
    judge: claude            # grades benchmark runs; must call tools; default: the investigate connector
    summarize: claude        # shortens old tool results; drafts the rubric of exported scenarios (see benchmark.md)
  prices:
    claude-sonnet-5-5: { inputPerMTok: 2, outputPerMTok: 10, cacheReadPerMTok: 0.1, cacheWritePerMTok: 2.5 }   # replaces the shipped price
```

Each provider accepts the shared settings above plus its own. A setting the
model does not support, such as an effort level it lacks, is an error.

Sidekick ships a price table for the models it knows, used for the cost of
every step and for the budgets. An entry under `prices` replaces the shipped
price of that model entirely, so set every field. A model with no price
counts as free in the budgets, so add a price for any model the table lacks.

A shipped price can change on a date. When a provider announces a price
rise, the table holds the new price with the day it starts (UTC), and each
step is priced at the time it is saved. For example, Gemini 4 Argon costs
$2 for input and $10 for output per million tokens until 2026-11-30, and $4
and $20 from 2026-12-01. No release is needed on that day. A price you set
under `prices` has no dates: it replaces the shipped price, rises included,
and stays as you set it.

#### The `anthropic` provider

```yaml
claude:
  provider: anthropic
  model: claude-sonnet-5-5                # required
  apiKeySecret: { name: sidekick-anthropic, key: apiKey }   # required
  baseURL: https://llm-proxy.internal     # optional, for a proxy
  maxTokens: 32000                        # default 64000, capped at the model's maximum
  effort: high                            # low, medium, high, xhigh or max
  thinking: { mode: adaptive }            # adaptive, or enabled with budgetTokens
  timeout: 10m                            # per call, default 10m
  retries: 3                              # retries of rate limits, overloads and timeouts
```

- **Known models.** Sidekick knows the current Claude models (Fable, Mythos,
  Opus, Sonnet and Haiku) and what each one supports. For any other model ID,
  describe the model in a `capabilities` block, or the config is rejected:

  ```yaml
  capabilities:
    toolCalling: true          # needed for the investigate and triage roles
    toolChoice: true           # the model accepts a forced tool choice
    thinkingModes: [adaptive]  # adaptive and/or enabled
    effortLevels: [low, medium, high]
    structuredOutput: true
    promptCaching: true
    contextTokens: 1000000
    maxOutputTokens: 128000
  ```

- **Effort** is `low`, `medium`, `high`, `xhigh` or `max`, where the model
  supports it. `none` and `minimal` are OpenAI levels and are rejected.
  Leaving it out keeps the model's own default.
- **Thinking.** Leaving `thinking` out keeps the model's default. `adaptive`
  lets the model decide how much to think. `enabled` with `budgetTokens`
  (at least 1024, and less than `maxTokens`) works only on the models that
  still take a fixed budget, such as Claude Haiku 4.5. Some models always
  think, for example Claude Sonnet 5.5 and Claude Opus 5.5; for them `mode: off` is an error, and
  `effort` is the way to make them think less.
- **Temperature** is not accepted. The Anthropic API version Sidekick uses
  rejects `temperature`, `top_p` and `top_k` on every model.
- **Prompt caching** uses the 5-minute cache. Cache reads and writes are
  priced at their own rates.
- **Base URL.** Every call carries the API key, so `baseURL` must use
  `https`. Plain `http` is accepted only for `localhost` and loopback
  addresses (127.0.0.0/8 and ::1), such as a proxy on the same machine.

#### The `openai` provider

One provider covers OpenAI's API and servers that speak it, such as Ollama,
vLLM and the llama.cpp server.

```yaml
gpt:
  provider: openai
  model: gpt-6.1-sol                       # required
  apiKeySecret: { name: sidekick-openai, key: apiKey }   # required without baseURL
  effort: high                             # the model's own levels, see below
  maxTokens: 32000                         # default: the model's own limit
  timeout: 10m                             # per call, default 10m
  retries: 3                               # retries of rate limits, overloads and timeouts

local:
  provider: openai
  model: qwen3.8:27b                       # a preset, see below
  baseURL: http://ollama:11434/v1          # an OpenAI-compatible server
  server: ollama                           # ollama, vllm or llamacpp; inferred when left out
  api: chat_completions                    # optional, see below
```

- **API.** Without `baseURL`, Sidekick calls the Responses API at
  api.openai.com. With `baseURL`, it calls Chat Completions, which every
  compatible server has. Set `api: responses` for a server or gateway that
  has the Responses API, such as Ollama 0.13.3 or later or a proxy in front
  of OpenAI. On api.openai.com, keep the Responses API: some GPT-6 models
  call tools through Chat Completions only at effort `none`.
- **Known models.** Sidekick knows GPT-6 Astra, GPT-6.1 Sol, GPT-6.1 Terra,
  GPT-6 Sol, GPT-6 Luna, GPT-5.6 (Sol, Terra and Luna), GPT-5.5, GPT-5.4,
  GPT-5.4 mini and GPT-5.4 nano, with their dated snapshots. They keep
  their capabilities behind a `baseURL` too, for example an LLM gateway.
  Any other model needs a `capabilities` block like the Anthropic one,
  without `thinkingModes`. For a model on a compatible server, set
  `toolChoice: true` only if the server honours a forced tool choice.
- **Effort** takes each model's own levels. GPT-6 Astra and GPT-6.1 Sol
  take `low` to `max`; the other GPT-6 and the GPT-5.6 models take `none`
  to `max`; GPT-5.5 and GPT-5.4 take `none` to `xhigh`. No current model
  takes `minimal`. On a compatible server, effort is sent as
  `reasoning_effort` only to a model that lists effort levels.
- **Thinking** is not accepted. OpenAI-style APIs control reasoning with
  `effort`; `none` turns it off on the models that have it.
- **Reasoning** is kept by Sidekick, not by OpenAI. Every call is sent with
  `store: false` and carries the whole conversation, including the
  encrypted reasoning of earlier turns, so it also works for organizations
  with Zero Data Retention.
- **Temperature** is sent as given. Some OpenAI reasoning models reject it.
- **Prompt caching** is automatic, so cache marks are not needed. Cache
  reads and writes are priced at their own rates, writes at the 30-minute
  price.
- **API key.** With `baseURL` the key is optional; leave `apiKeySecret` out
  for a local server that needs none. Without `apiKeySecret`, Sidekick sends
  no key at all; it does not read `OPENAI_API_KEY` from the environment.
- **Base URL.** With `apiKeySecret` set, `baseURL` must use `https`, so the
  key never crosses the network unencrypted. Plain `http` is accepted with a
  key only for `localhost` and loopback addresses (127.0.0.0/8 and ::1).
  Without `apiKeySecret`, plain `http` works to any host, for example an
  Ollama or vLLM server elsewhere in the cluster that needs no key.

##### Local models for free setups

Sidekick ships the capabilities of two open-weight models, so they need no
`capabilities` block:

| Preset | Model ID on vLLM or llama.cpp | Ollama tags |
|---|---|---|
| Qwen3.8-27B | `Qwen/Qwen3.8-27B`, `Qwen/Qwen3.8-27B-FP8` | `qwen3.8`, `qwen3.8:latest`, `qwen3.8:27b` and its variants, such as `qwen3.8:27b-q4_K_M` |
| Gemma 4 31B | `google/gemma-4-31B-it` | `gemma4:31b` and its variants, such as `gemma4:31b-it-qat` (`gemma4` alone is a smaller model) |

Both call tools and have a 256K token context. Run them with a context of
at least 65,536 tokens; an investigation does not fit in less. Qwen3.8 takes
effort `low`, `medium` or `xhigh`.

Whether a model may be forced to call a tool depends on the server:

- **vLLM** (`server: vllm`) honours a forced tool choice. Start it with tool
  calling on and a long enough context:

  ```sh
  vllm serve Qwen/Qwen3.8-27B --max-model-len 65536 \
    --enable-auto-tool-choice --tool-call-parser qwen3_xml --reasoning-parser qwen3
  vllm serve google/gemma-4-31B-it --max-model-len 65536 \
    --enable-auto-tool-choice --tool-call-parser gemma4 --reasoning-parser gemma4
  ```

- **Ollama** (`server: ollama`) ignores `tool_choice`. Unless
  `OLLAMA_CONTEXT_LENGTH` is set, it picks the context from the video
  memory: 4K tokens below 24 GiB and 32K below 48 GiB, both too small. A
  prompt that does not fit is cut rather than refused. Start it with
  `OLLAMA_CONTEXT_LENGTH=65536 ollama serve`, and check the `CONTEXT`
  column of `ollama ps`. `sidekick doctor` checks it too while the model is
  loaded.
- **llama.cpp server** (`server: llamacpp`) needs `--jinja` for tool
  calling. Give the model a preset ID with `--alias`:
  `llama-server --jinja -c 65536 --alias Qwen/Qwen3.8-27B -m <file>.gguf`.

Without `server`, Sidekick assumes Ollama when the model is an Ollama tag
(the `name:tag` form) or the address uses port 11434, and otherwise assumes
a server that ignores a forced tool choice. That is safe: when the model
ends a turn without calling a tool, the investigation reminds it to call
`submit_verdict`.
`sidekick doctor` warns when a preset has no `server` set, and asks the
server whether it is Ollama, so it can print the line to add.

Local models have no price, so they count as free in the budgets.

#### The `azure` provider

The `azure` provider calls model deployments on Azure OpenAI and Microsoft
Foundry, through their v1 API, which takes OpenAI's request shapes and
needs no `api-version`.

```yaml
gpt-on-azure:
  provider: azure
  model: gpt-6.1-sol                       # required: the model the deployment runs
  endpoint: https://my-resource.openai.azure.com   # required; or https://my-resource.services.ai.azure.com
  deployment: prod-gpt                     # the deployment name; default: the same as model
  auth: entra                              # entra, or apiKey (the default) with apiKeySecret
  effort: high                             # the model's own levels, as for the openai provider
  maxTokens: 32000                         # default: the model's own limit
  timeout: 10m                             # per call, default 10m
  retries: 3                               # retries of rate limits, overloads and timeouts

gpt-with-a-key:
  provider: azure
  model: gpt-5.4
  endpoint: https://my-resource.openai.azure.com
  apiKeySecret: { name: sidekick-azure, key: apiKey }
```

- **Endpoint** is the resource address, as the Azure portal shows it. It
  must use https, except to localhost; leave out paths such as
  `/openai/deployments/...` and the `api-version` query of older SDKs.
- **Credentials.** With `auth: entra`, Sidekick gets Microsoft Entra ID
  tokens from the default Azure credential chain: AKS Workload Identity in
  the cluster (see [Cloud credentials](install.md#cloud-credentials)), a
  managed identity, the `AZURE_*` environment variables, or the Azure CLI
  for local runs. The identity needs the Cognitive Services OpenAI User
  role on the resource. Otherwise the key in `apiKeySecret` is sent in the
  `api-key` header, read again on every call.
- **Known models** are named by their model ID in `model`, whatever the
  deployment is called:
  - the OpenAI models of the `openai` provider;
  - these Foundry models from other providers: `Kimi-K2.6`, `Kimi-K2.5`,
    `grok-4.7`, `grok-4.6` and `grok-4.3`, which call tools, and
    `DeepSeek-V4-Pro`, `DeepSeek-V4-Flash` and `DeepSeek-V3.2`, which
    cannot be the `investigate` or `triage` model. DeepSeek's thinking
    models fail a tool call turn unless the reasoning of earlier turns is
    sent back, which Sidekick does not do on Chat Completions; use them for
    `summarize`. Foundry does not say whether these models honour a forced
    tool choice, so Sidekick does not force one; when the model ends a turn
    without calling a tool, the investigation reminds it to call
    `submit_verdict`. They take no `effort`. MAI-Thinking-1 is served under
    another path (`/mai/v1`) and does not work with this provider.

  For any other model, add a `capabilities` block like the `openai` one.
- **API.** OpenAI models use the Responses API by default, as Microsoft
  recommends; every other model uses Chat Completions. Set `api` to choose.
  Reasoning is kept by Sidekick, as for the `openai` provider, and is not
  shared with OpenAI's own API. Chat Completions does not carry reasoning
  back, so models that return reasoning there start each turn without it.
- **Thinking** is not accepted; use `effort`.
- **Errors.** A prompt that Azure's content filter blocks, and a
  deployment that does not exist, fail the step without retries.
- **Prices.** The shipped OpenAI prices apply, by `model`, and the shipped
  table also has DeepSeek-V4-Pro, DeepSeek-V4-Flash, Kimi-K2.6 and
  Kimi-K2.5. They are the prices of Global Standard deployments;
  Data Zone and Regional deployments cost 10% more, so set them with
  `prices`. The Grok models and DeepSeek-V3.2 have no shipped price; set
  one with `prices`, or their calls count as free in the budgets.

#### The `bedrock` provider

The `bedrock` provider calls models on Amazon Bedrock through the Converse
API.

```yaml
claude-on-aws:
  provider: bedrock
  model: global.anthropic.claude-opus-5-5   # required: a model ID or inference profile ID
  region: us-east-1                         # required
  endpoint: https://vpce-0abc.bedrock-runtime.us-east-1.vpce.amazonaws.com   # optional, for a VPC endpoint; https, or http only to localhost
  maxTokens: 32000                          # default 64000 for known models, capped at the model's maximum
  effort: high                              # low, medium, high, xhigh or max
  thinking: { mode: adaptive }              # adaptive, or enabled with budgetTokens
  timeout: 10m                              # per call, default 10m
  retries: 3                                # retries of throttling, overloads and timeouts
```

- **Credentials** come from the default AWS credential chain: IRSA or EKS
  Pod Identity on EKS, otherwise environment variables, the shared config
  files or the instance role. There is no key in the config;
  [Cloud credentials](install.md#cloud-credentials) shows how to give the
  pod an IAM role. The role
  needs `bedrock:InvokeModel` and `bedrock:InvokeModelWithResponseStream`
  on the model or inference profile. A Bedrock API key in
  `AWS_BEARER_TOKEN_BEDROCK` is not used.
- **Known models.** Sidekick knows the current Claude models on Bedrock,
  under their Bedrock IDs (for example `anthropic.claude-opus-5-5`,
  `anthropic.claude-opus-4-6-v1` or
  `anthropic.claude-haiku-4-5-20251001-v1:0`), and under each geographic or
  global inference profile ID (`us.`, `us-gov.`, `eu.`, `apac.`, `au.`,
  `jp.`, `in.` or `global.` before the ID). Most current Claude models are
  served only through an inference profile, so use one. For any other
  model ID, an ARN or an application inference profile, describe the model
  in a `capabilities` block, with the same keys as for the `anthropic`
  provider. Thinking modes and effort levels can be declared only for
  Anthropic models (IDs containing `anthropic.`); other model families,
  such as Amazon Nova or Meta Llama, run with tools only.
- **Effort and thinking** follow the Claude rules described for the
  `anthropic` provider, and are sent in the Converse
  `additionalModelRequestFields`.
- **Temperature** is passed to the model as given. Claude rejects it while
  it thinks.
- **Tool choice.** Converse cannot turn tools off for one call, so tool
  choice `none` is an error. While a Claude model thinks it cannot be made
  to call a given tool; such a request is sent with tool choice `auto`.
- **Prompt caching** uses Bedrock cache points with the 5-minute lifetime,
  at most 4 per call, on models that support it.
- **Prices.** The shipped price table has every known model under its bare
  ID and each inference profile. A `global.` profile costs the same as the
  Claude API; in-Region and geographic profiles cost 10% more. GovCloud
  prices are not shipped; set them with `prices`.
- **Long outputs.** A call is streamed when its output limit is above
  21,333 tokens, as the default limit is, so long answers do not hit HTTP
  timeouts.

#### The `google` provider

The `google` provider calls Gemini models on the Gemini API (Google AI
Studio) or on Vertex AI, which Google now calls Gemini Enterprise Agent
Platform.

```yaml
gemini:
  provider: google
  model: gemini-3.8-flash                  # required
  backend: gemini                          # gemini (the default) or vertex
  apiKeySecret: { name: sidekick-gemini, key: apiKey }   # required with gemini
  effort: medium                           # the model's own levels, see below
  maxTokens: 32000                         # default: the model's own limit
  timeout: 10m                             # per call, default 10m
  retries: 3                               # retries of rate limits, overloads and timeouts

gemini-on-gcp:
  provider: google
  backend: vertex
  model: gemini-3.8-flash
  project: my-project                      # required with vertex
  location: global                         # default global; or a region such as europe-west4, or us or eu
  baseURL: https://psc.example.internal    # optional, for a proxy or Private Service Connect; https, or http only to localhost
```

- **Credentials.** With `backend: gemini`, the key comes from the Secret;
  it is read again on every call, so a rotated key is used without a
  restart. With `backend: vertex` there is no key in the config: Sidekick
  uses Application Default Credentials, which is GKE Workload Identity in
  the cluster (see [Cloud credentials](install.md#cloud-credentials)), or
  a credentials file named by `GOOGLE_APPLICATION_CREDENTIALS`, for example
  for workload identity federation from another cloud. The Google service
  account needs the Vertex AI User role (`roles/aiplatform.user`) on the
  project. Other `GOOGLE_*` and `GEMINI_*` environment variables, such as
  `GOOGLE_API_KEY`, are not used.
- **Known models.** Sidekick knows Gemini 4 Argon, Gemini 3.8 Flash, Gemini
  3.6 Flash, Gemini 3.5 Flash-Lite, Gemini 3.1 Flash-Lite, Gemini 3.1 Pro
  Preview, Gemini 3 Flash Preview, Gemini 2.5 Pro, Gemini 2.5 Flash and
  Gemini 2.5 Flash-Lite, under their model IDs (`gemini-3.8-flash` and so
  on) on both backends. The preview models are served only from the
  `global` location. For any other model, describe it in a `capabilities`
  block with the same keys as for the `anthropic` provider. A Gemini model
  takes either `effortLevels` or a `thinkingModes` list with `enabled`,
  not both.
- **Effort** sets the thinking level of Gemini 3 and later models, with
  each model's own levels: Gemini 4 Argon, Gemini 3.8 Flash and Gemini 3.1
  Pro take `low`, `medium` and `high`; the others also take `minimal`.
  `xhigh`, `max` and `none` are not Gemini levels. The Gemini 2.5 models
  take no effort.
- **Thinking.** Gemini 3 and later models always think, so `mode: off` is
  an error for them; lower `effort` instead, since `minimal` still thinks a
  little. The Gemini 2.5 models take `adaptive`, where the model picks its
  own budget, and `enabled` with `budgetTokens`: 128 to 32,768 on 2.5 Pro,
  1 to 24,576 on 2.5 Flash and 512 to 24,576 on 2.5 Flash-Lite. `off` works
  on 2.5 Flash and 2.5 Flash-Lite, not on 2.5 Pro. Gemini 2.5 Flash-Lite
  does not think unless `thinking` is set.
- **Reasoning.** The thought signatures Gemini returns are kept by
  Sidekick and sent back in the part they came in, as Gemini 3 requires
  for tool calls.
- **Tool choice.** Every known model can be forced to call a tool, or any
  tool, and tool choice `none` turns tools off for one call.
- **Prompt caching** is automatic, so cache marks are not needed. Cache
  reads are priced at their own rate, and Google charges nothing to write
  the cache.
- **Labels.** On Vertex AI, request metadata is sent as labels, which show
  in Cloud Billing. Entries that are not valid labels are left out.
- **Prices.** The shipped price table has the Standard paid prices of the
  Gemini API, which are also the prices of Vertex AI's `global` location.
  Regional and multi-region locations cost 10% more on Gemini 3 and later
  models, and the Gemini API free tier costs nothing; set them with
  `prices`.
- **Free tier.** The Gemini API free tier works with a key from a project
  without billing, with low rate limits. Google may use free-tier prompts
  and answers to improve its products, and people may read them, so use it
  with test data only.

### `sources`

Sources give the investigation tools to call. Each one has a `type`, and the
other keys depend on that type.

```yaml
sources:
  signoz:
    type: mcp
    url: https://signoz-mcp.internal/mcp
    headers:
      Authorization: { secret: { name: signoz-mcp, key: token }, prefix: "Bearer " }
    tools:
      allow: ["signoz_search_logs", "signoz_query_metrics"]
      deny: ["*delete*", "*create*", "*update*"]
```

Every source type takes these keys as well:

| Key | Default | Meaning |
|---|---|---|
| `tools.allow` | every tool | Globs (`path.Match` syntax) of the tool names the model may see. |
| `tools.deny` | none | Globs of tool names the model never sees. Deny wins over allow. |
| `tools.rename` | none | Maps a tool name to the name the model sees after the source prefix. |
| `tools.trustAnnotations` | `true` | Whether the source's own read-only hints decide which tools write. Only some source types have such hints. |
| `timeout` | `30s` | How long one tool call may take. |
| `concurrency` | `4` | How many calls to the source may run at once on one replica. |

The model sees a source tool as `<source>__<tool>`, for example
`signoz__search_logs`. That name must fit 64 characters, the shortest limit
of the supported model providers, and use only letters, digits, `_` and
`-`. `sidekick config validate` checks the names in `tools.rename`. A tool
whose name is too long and is not renamed is left out when the source
lists its tools, and the reason is logged; rename it to bring it back.
Tools the source marks as writing are never shown to the model, even when
`tools.allow` names them.

#### MCP sources (`type: mcp`)

An MCP source connects to one [Model Context Protocol](https://modelcontextprotocol.io)
server.

| Key | Meaning |
|---|---|
| `transport` | `http` (streamable HTTP, the default), `sse` (the legacy transport, for older servers) or `stdio`. |
| `url` | The server's endpoint. Required for `http` and `sse`. |
| `headers` | Headers sent with every request, each either `{ value: <text> }` or `{ secret: { name, key }, prefix: <text> }`. Only for `http` and `sse`. |
| `command` | The program and its arguments, for example `["/usr/local/bin/mcp-git", "--repository", "/work"]`. Required for `stdio`. |
| `env` | Environment variables of the `stdio` process, in the same form as `headers`. The process gets these and `PATH`, nothing else from Sidekick's environment. |

MCP sources authenticate with tokens from Secrets, sent in `headers` (or
`env` for `stdio`). They do not support OAuth yet, and an `oauth` key is
rejected. [Authenticating MCP sources](sources/mcp-authentication.md)
explains why, and has examples for SigNoz, Datadog, Sentry, Linear, GitHub
and servers behind your own identity provider.

```yaml
sources:
  git:
    type: mcp
    transport: http
    url: http://127.0.0.1:8101/mcp
    tools: { allow: ["git_log", "git_show", "git_diff"] }
    timeout: 20s
```

How it behaves:

- **Start-up never waits for the server.** Sidekick connects in the
  background, within 10 seconds per attempt, and retries with a growing
  wait (up to a minute) until it works. A lost connection is retried the
  same way. While a source is not connected, its tools are left out of new
  investigations and the report lists it as unavailable. The
  `sidekick_source_up` metric shows the state of each source.
- **Tools** are listed when Sidekick connects, when the server says its
  list changed, and every 10 minutes. An investigation keeps the list it
  started with. A server may list at most 500 tools, and descriptions are
  cut at 2,048 bytes.
- **Writing tools.** With `tools.trustAnnotations: true`, the default, a
  tool is left out when the server sends annotations for it that do not
  say `readOnlyHint: true`, or that say `destructiveHint: true`. A tool
  the server sends no annotations for is kept, so for such tools only
  `tools.allow` and `tools.deny` decide: list in `tools.allow` exactly the
  tools that only read. Each tool left out is logged at info level with
  the reason. With `tools.trustAnnotations: false` the annotations are
  ignored and only the globs decide.
- **Timeout.** With `http` and `sse`, `timeout` may be at most `60s`,
  because the HTTP client stops waiting for response headers after 60
  seconds; `sidekick config validate` rejects a longer one. `stdio` takes
  any timeout.
- **Results.** Text is joined, structured content is kept as JSON, links to
  `http` and `https` resources become evidence links in the report, and
  images and audio are dropped. Results over 256 KiB are cut. A tool that
  reports an error passes its message to the model as a tool error.
- `stdio` runs the server inside the Sidekick container, so it works only
  with a server built into a custom Sidekick image. Run other stdio servers
  in a sidecar that serves them over HTTP, and use `transport: http`.

#### Kubernetes sources (`type: kubernetes`)

A Kubernetes source reads one cluster with the pod's service account, or
with a kubeconfig file from a Secret. Its tools read events, workload
status, pods with problems, rollout history, nodes and, with `logs: true`,
pod logs. [The Kubernetes source](sources/kubernetes.md) lists its keys and
tools, and the RBAC it needs.

| Key | Meaning |
|---|---|
| `namespaces` | The namespaces the tools may read. Empty means every namespace. |
| `logs` | Turns on `get_pod_logs`. Off by default. |
| `consoleURL` | A Go template for evidence links, with `.Kind`, `.Resource`, `.Namespace` and `.Name`. Without it, evidence is the matching `kubectl` command. |
| `kubeconfigSecret` | `{ name, key }` of a kubeconfig file, for another cluster. |
| `context` | The kubeconfig context. Only with `kubeconfigSecret`. |

```yaml
sources:
  k8s:
    type: kubernetes
    namespaces: [prod, staging]
```

#### GitHub sources (`type: github`)

A GitHub source reads repositories through the GitHub REST API as a GitHub
App installation, so investigations can check what changed. Its tools only
read. [GitHub source](sources/github.md) lists the permissions the app
needs and how to set it up.

| Key | Meaning |
|---|---|
| `app.appID` | The GitHub App's ID. Required. |
| `app.installationID` | The ID of the app's installation on the account that owns the repositories. Required. |
| `app.privateKeySecret` | `{ name, key }` of the Secret that holds the app's private key, in PEM form. Required. |
| `repos` | The repositories the tools may read, as `owner/name` globs (`path.Match` syntax), for example `acme/*`. Required; a call for any other repository fails. |
| `serviceRepos` | Maps a service name to its repository, so a tool can be called with the service an alert names. Each repository must be one that `repos` allows. |
| `baseURL` | The REST API root, for GitHub Enterprise Server, for example `https://github.example.com/api/v3/`. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses. Default: github.com. |

```yaml
sources:
  github:
    type: github
    app:
      appID: 123456
      installationID: 7890123
      privateKeySecret: { name: sidekick-github, key: private-key.pem }
    repos: ["acme/*"]
    serviceRepos:
      checkout-api: acme/checkout
```

The tools, as the model sees them with the source name `github`:

| Tool | Returns |
|---|---|
| `github__list_commits` | Commits since a time, newest first, optionally on a branch or touching a path. |
| `github__get_commit` | One commit: message, author, parents, changed files with line counts, and patches. |
| `github__list_pull_requests` | Pull requests opened or merged since a time. |
| `github__list_releases` | Releases with their dates, and optionally the newest tags. |
| `github__list_workflow_runs` | GitHub Actions runs, such as deploys, with status, conclusion and times. |
| `github__get_file` | A file at a ref, or the entries of a directory. |
| `github__compare` | The commits and changed files between two refs. |

How it behaves:

- **Start-up never waits for GitHub.** The first tool call makes the first
  installation token. Tokens are replaced five minutes before they expire,
  and once more when GitHub refuses one. The private key is read from its
  Secret for each new token, so a rotated key needs no restart.
- **Repository.** Every tool takes `repo` (`owner/name`) or `service` (a
  name from `serviceRepos`). A repository that `repos` does not allow, an
  unknown service, or a ref or path that does not look like one is a tool
  error, and nothing is sent to GitHub.
- **Results** are JSON, and link to the GitHub pages of the commits, pull
  requests, releases, runs and files they name, which become evidence links
  in the report. Lists return up to `limit` items (default 30, at most 100)
  and read at most 10 pages. Commit messages in lists are cut at 1 KiB,
  the patches of one commit at 64 KiB in all, and files at 256 KiB; binary
  files and files over 1 MB are left out.
- **No data.** A repository, ref or path GitHub does not have, an empty
  repository, and an empty list are reported as no data. GitHub gives the
  same "not found" for a repository the app is not installed on.
- **Errors.** Other refusals, such as a ref GitHub cannot resolve or a
  repository the app lacks a permission for, go to the model as tool
  errors with GitHub's message. Server errors, rate limits and a token
  GitHub refuses twice count as failures of the source.
- **Timeout.** `timeout` may be at most `60s`, because the HTTP client
  stops waiting for response headers after 60 seconds; `sidekick config
  validate` rejects a longer one.

#### GitLab sources (`type: gitlab`)

A GitLab source reads projects on gitlab.com or a self-managed GitLab through
the GitLab REST API, with an access token, so investigations can check what
changed. Its tools only read. [GitLab source](sources/gitlab.md) lists the
token's scope and role and how to make the token.

| Key | Meaning |
|---|---|
| `tokenSecret` | `{ name, key }` of the Secret that holds the access token: a project, group or personal access token with the `read_api` scope and the Reporter role. Required. |
| `projects` | The projects the tools may read, as full paths that may hold globs, for example `acme/checkout`, `acme/platform/*` or `acme/**`. `*` and `?` stay inside one part of the path, and `**` is a whole part that matches any number of parts. Required; a call for any other project fails. |
| `serviceProjects` | Maps a service name to its project, so a tool can be called with the service an alert names. Each project must be one that `projects` allows. |
| `baseURL` | The root of the GitLab, for example `https://gitlab.example.com`, without `/api/v4`. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses. Default: `https://gitlab.com`. |

```yaml
sources:
  gitlab:
    type: gitlab
    tokenSecret: { name: sidekick-gitlab, key: token }
    projects: ["acme/checkout", "acme/platform/*"]
    serviceProjects:
      checkout-api: acme/checkout
```

The tools, as the model sees them with the source name `gitlab`:

| Tool | Returns |
|---|---|
| `gitlab__list_commits` | Commits since a time, newest first, optionally on a ref or touching a path. |
| `gitlab__get_commit` | One commit: message, author, parents, changed files with line counts, and patches. |
| `gitlab__list_merge_requests` | Merge requests opened or merged since a time. |
| `gitlab__list_releases` | Releases with their dates, and optionally the newest tags. |
| `gitlab__list_pipelines` | Pipelines, newest first, with status, source, ref and times. |
| `gitlab__get_file` | A file at a ref, or the entries of a directory. |
| `gitlab__compare` | The commits and changed files between two refs. |
| `gitlab__list_deployments` | Deployments of a project or one environment, with status, ref, commit, who deployed and times. |

How it behaves:

- **Start-up never waits for GitLab.** The first tool call is the first
  request. The token is read from its Secret for each request, so a rotated
  token needs no restart.
- **Project.** Every tool takes `project` (the full path) or `service` (a
  name from `serviceProjects`). A project that `projects` does not allow, an
  unknown service, a numeric project ID, or a ref or path that does not look
  like one is a tool error, and nothing is sent to GitLab.
- **Results** are JSON, and link to the GitLab pages of the commits, merge
  requests, releases, pipelines, deployments and files they name, which
  become evidence links in the report. Lists return up to `limit` items
  (default 30, at most 100) and read at most 10 pages. Commit messages in
  lists are cut at 1 KiB, the patches of one commit at 64 KiB in all, and
  files at 256 KiB; binary files and files over 1 MB are left out.
- **No data.** A project, ref or path GitLab does not have, and an empty
  list, are reported as no data. GitLab gives the same "not found" for a
  project the token cannot see.
- **Errors.** Other refusals, such as a ref GitLab cannot resolve or a call
  the token's role may not make, go to the model as tool errors with GitLab's
  message. Server errors, rate limits and a refused token count as failures
  of the source.
- **Timeout.** `timeout` may be at most `60s`, because the HTTP client
  stops waiting for response headers after 60 seconds; `sidekick config
  validate` rejects a longer one.

#### Backstage sources (`type: backstage`)

A Backstage source reads a Backstage software catalog with a static token,
so investigations can find a service's owner, system and dependencies. Its
tools only read. [The Backstage source](sources/backstage.md) lists its
tools and how to give Sidekick a token.

| Key | Meaning |
|---|---|
| `baseURL` | The root of the Backstage backend, for example `https://backstage.example.com`. Required. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses. |
| `tokenSecret` | `{ name, key }` of the Secret that holds the token. Required. |
| `appURL` | The root of the Backstage web app, for evidence links. Default: `baseURL`. |
| `namespace` | The catalog namespace of a name given without one. Default: `default`. |

```yaml
sources:
  backstage:
    type: backstage
    baseURL: https://backstage.example.com
    tokenSecret: { name: sidekick-backstage, key: token }
```

The tools, as the model sees them with the source name `backstage`, are
`backstage__get_service`, `backstage__get_owner` and
`backstage__get_system`.

#### Elasticsearch and OpenSearch sources (`type: elasticsearch`)

An Elasticsearch source searches the logs an Elasticsearch or OpenSearch
cluster keeps, through the `_search` and `_field_caps` APIs, with an API
key or a user and password. Its tools only read, and only the index
patterns it lists. [The Elasticsearch and OpenSearch
source](sources/elasticsearch.md) lists every key, the read-only role it
needs and how to set up links to Kibana or OpenSearch Dashboards.

| Key | Meaning |
|---|---|
| `flavor` | `elasticsearch` or `opensearch`. Default: `elasticsearch`. |
| `baseURL` | The root of the cluster's REST API, for example `https://logs.example.com:9200`. Required. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses. |
| `apiKeySecret` | `{ name, key }` of the Secret that holds an Elasticsearch API key, encoded. Only with `flavor: elasticsearch`. |
| `basicAuth` | `username` and `passwordSecret`, `{ name, key }` of the Secret that holds the password. Set exactly one of `apiKeySecret` and `basicAuth`. |
| `indexPatterns` | The index patterns the tools may read, such as `logs-*`. Required; a call for an index no pattern covers fails before anything is sent. |
| `timeField`, `messageField` | The fields that hold each log's time and message. Default: `@timestamp` and `message`. |
| `defaultFields` | The fields `search_logs` returns when the call names none. Default: a few Elastic Common Schema fields such as `log.level` and `service.name`. |
| `discoverURL` | A Go template for evidence links to Kibana Discover or OpenSearch Dashboards, with `.Index`, `.Query`, `.From` and `.To`. |

```yaml
sources:
  logs:
    type: elasticsearch
    flavor: opensearch
    baseURL: https://opensearch.example.com:9200
    basicAuth:
      username: sidekick
      passwordSecret: { name: sidekick-opensearch, key: password }
    indexPatterns: [logs-*]
```

The tools, as the model sees them with the source name `logs`:

| Tool | Returns |
|---|---|
| `logs__search_logs` | The newest logs that match a Lucene query string in a time range, with the chosen fields. |
| `logs__count_logs` | Counts of matching logs over time, optionally split by the 10 most common values of one field. |
| `logs__list_fields` | The fields of an index pattern with their types. |

How it behaves:

- **Start-up never waits for the cluster.** The first tool call is the
  first request, and the API key or password is read from its Secret for
  each request.
- **Input.** An index outside `indexPatterns`, an `_index` clause in the
  query string that points outside it, a range longer than 7 days or with
  no `since`, or input that does not fit is a tool error, and nothing is
  sent.
- **Results** are JSON. `search_logs` returns at most 200 hits, with long
  values cut, and says when hits were left out. Searches and counts link
  to Kibana or OpenSearch Dashboards when `discoverURL` is set.
- **No data.** A search that matches nothing and an index that does not
  exist are reported as no data.
- **Errors.** A query string the cluster cannot parse or a privilege the
  role lacks goes to the model as a tool error with the cluster's message.
  Refused credentials, rate limits and server errors count as failures of
  the source.
- **Timeout.** `timeout` may be at most `60s`, because the HTTP client
  stops waiting for response headers after 60 seconds; `sidekick config
  validate` rejects a longer one.

#### Google Cloud sources (`type: gcp`)

A Google Cloud source reads Google Cloud projects through the Cloud
Logging, Cloud Monitoring, GKE and Personalized Service Health REST APIs,
with Application Default Credentials (Workload Identity on GKE). Its tools
only read. [Google Cloud source](sources/gcp.md) lists the permissions the
identity needs and how to set it up.

| Key | Meaning |
|---|---|
| `projects` | The IDs of the projects the tools may read. Required; a call for any other project fails before anything is sent. |
| `credentialsSecret` | `{ name, key }` of a Secret that holds a credential file: a workload identity federation configuration, or a service account key. Default: Application Default Credentials. |

```yaml
sources:
  gcp:
    type: gcp
    projects: [shop-prod, shop-staging]
```

The tools, as the model sees them with the source name `gcp`:

| Tool | Returns |
|---|---|
| `gcp__query_logs` | Cloud Logging entries that match a Logging query language filter, newest first. |
| `gcp__query_metrics` | Cloud Monitoring time series for a monitoring filter, optionally aligned and reduced. |
| `gcp__query_promql` | The series of a PromQL range query on Cloud Monitoring metrics. |
| `gcp__list_alerts` | Cloud Monitoring alerts open at some time in a range, with their policy and severity. |
| `gcp__list_changes` | Changes from the Admin Activity and System Event audit logs: deploys, config changes, upgrades. |
| `gcp__get_gke_cluster` | A GKE cluster's status, versions, node pools and recent operations, or the list of clusters. |
| `gcp__list_service_health_events` | Google Cloud incidents that Personalized Service Health says affect the project. |

How it behaves:

- **Start-up never waits for Google Cloud.** The first tool call finds the
  credentials and makes the first token. Tokens are kept until shortly
  before they expire; a credential file in `credentialsSecret` is read
  again for each new token.
- **Project.** Every tool takes `project`, which may be left out when
  `projects` lists one. A project not in `projects`, or input that does
  not fit, such as a range longer than 7 days, is a tool error, and
  nothing is sent.
- **Queries.** The filter of `query_logs` and `query_metrics` and the
  query of `query_promql` go to Google Cloud as the model wrote them, so
  the identity's roles are what limit what can be read.
- **Results** are JSON with links to the Logs Explorer query, Metrics
  Explorer, the alert, the cluster or the Service Health page, which
  become evidence links in the report. Lists are capped as
  [Google Cloud source](sources/gcp.md#limits) describes, and say when
  they were cut.
- **No data.** A query that matches nothing, and a project or cluster
  Google Cloud does not have, are reported as no data.
- **Errors.** A refused request, such as a filter Google Cloud cannot
  parse or a permission the identity lacks, goes to the model as a tool
  error with Google Cloud's message. A refused token, rate limits, server
  errors and credentials that cannot be found count as failures of the
  source.
- **Timeout.** `timeout` may be at most `60s`, because the HTTP client
  stops waiting for response headers after 60 seconds; `sidekick config
  validate` rejects a longer one.

#### Postgres sources (`type: postgres`)

A Postgres source reads the statistics views of Postgres databases your
services use, not Sidekick's own: slow statements, running queries, lock
waits and replication lag. Its tools only read, and run fixed SQL in
read-only transactions. [Postgres source](sources/postgres.md) lists the
role it needs, the TLS rules for its URL, and how to turn on
`pg_stat_statements`.

| Key | Meaning |
|---|---|
| `databases` | The databases the tools may read. Required, at least one. The model picks one by `name` on each call. |
| `databases[].name` | What the model calls the database, for example `orders`: a lower-case letter, then lower-case letters, digits, `_` and `-`, at most 63 in all. Each name is used once. |
| `databases[].urlSecret` | `{ name, key }` of the Secret that holds the database's `postgres://` URL, with the role and its password. Required. |
| `databases[].allowInsecure` | Accept a URL that does not verify the server's TLS certificate, and log a warning each time Sidekick connects with it. Default `false`, so the URL must set `sslmode=verify-full` or `verify-ca` unless every host is local. |
| `omitQueryText` | Leave the text of queries out of every result. Default `false`. |
| `statementTimeout` | How long one query may run, set as `statement_timeout` on each transaction. Default `10s`; it must be shorter than `timeout`. |

```yaml
sources:
  db:
    type: postgres
    databases:
      - name: orders
        urlSecret: { name: sidekick-orders-db, key: url }
      - name: orders-replica
        urlSecret: { name: sidekick-orders-replica-db, key: url }
    omitQueryText: false
```

The tools, as the model sees them with the source name `db`:

| Tool | Returns |
|---|---|
| `db__slow_queries` | The statements with the highest mean or total execution time, from `pg_stat_statements`. |
| `db__running_queries` | Sessions running a query or holding a transaction open, longest first, with wait events and blockers. |
| `db__lock_waits` | Sessions waiting for a lock, the sessions blocking them, and the heads of the blocking chains. |
| `db__replication_lag` | On a primary, each standby's lag in bytes and time; on a standby, its own replay delay. |

How it behaves:

- **Start-up never waits for a database.** The first call for a database
  reads its URL Secret and connects. The Secret is read on every call, and
  new connections are made when the URL changed, so a rotated password
  needs no restart.
- **Safety.** Each call runs in a `READ ONLY` transaction with
  `statement_timeout` set to `statementTimeout` and `lock_timeout` set to
  1 second. Sidekick refuses to connect as a superuser, and warns when the
  role is not a member of `pg_monitor`.
- **Results** are JSON. Each tool returns up to `limit` rows (default 20,
  at most 100), and the text of each query is cut at 2 KiB. A view with
  nothing to show, or a database without `pg_stat_statements` for
  `slow_queries`, is reported as no data.
- **Errors.** An error Postgres reports for a query, such as a timeout or
  a missing privilege, goes to the model as a tool error with its message.
  A database that cannot be reached, a refused login and a refused URL
  count as failures of the source.

#### Azure sources (`type: azure`)

An Azure source reads Azure subscriptions and Log Analytics workspaces
through the Log Analytics query API and Azure Resource Manager, with AKS
Workload Identity or a client secret. Its tools only read. [Azure
source](sources/azure.md) lists the role the identity needs and how to set
it up.

| Key | Meaning |
|---|---|
| `subscriptions` | The IDs of the subscriptions the tools may read. Required; a call for any other subscription, or a resource in one, fails before anything is sent. |
| `workspaces` | The Log Analytics workspaces `query_logs` may read, each with its workspace `id` (a GUID) and its `resourceID`. The model names a workspace by the last part of its resource ID. |
| `tenantID` | The Microsoft Entra tenant of the identity. Default: `AZURE_TENANT_ID`, which the AKS webhook sets. |
| `clientID` | The client ID of the identity. Default: `AZURE_CLIENT_ID`, which the AKS webhook sets. |
| `clientSecret` | `{ name, key }` of a Secret that holds an app registration's client secret, for running outside AKS. Needs `tenantID` and `clientID`. Default: AKS Workload Identity. |

```yaml
sources:
  azure:
    type: azure
    subscriptions: [11111111-2222-3333-4444-555555555555]
    workspaces:
      - id: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
        resourceID: /subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/shop/providers/Microsoft.OperationalInsights/workspaces/prod-logs
```

The tools, as the model sees them with the source name `azure`:

| Tool | Returns |
|---|---|
| `azure__query_logs` | The tables and rows of a KQL query on a Log Analytics workspace. |
| `azure__query_metrics` | Azure Monitor platform metrics of one resource, optionally split by dimension. |
| `azure__list_alerts` | Azure Monitor alerts that started in a range, with severity, state and target. |
| `azure__list_changes` | Activity Log writes, deletes and actions: deployments and config changes, with who made them. |
| `azure__get_resource_health` | The Resource Health of one resource, or the subscription's unavailable resources and Azure service health events. |
| `azure__get_aks_cluster` | An AKS cluster's state, versions, upgrade channel and node pools, or the list of clusters. |

How it behaves:

- **Start-up never waits for Azure.** The first tool call sets up the
  credential and makes the first token. Tokens are kept until five
  minutes before they expire; a client secret is read again for each new
  token.
- **Scope.** Tools take `subscription`, which may be left out when
  `subscriptions` lists one, a full `resource` ID, or a `workspace`. Any
  of them outside the config, or input that does not fit, such as a range
  longer than 7 days, is a tool error, and nothing is sent.
- **Queries.** The KQL of `query_logs` and the dimension filter of
  `query_metrics` go to Azure as the model wrote them, so the identity's
  role assignments are what limit what can be read. A query that calls
  `workspace()`, `app()`, `resource()`, `adx()`, `arg()`, `cluster()` or
  `database()`, or uses `externaldata`, is refused before it is sent,
  because those reach beyond the workspace.
- **Results** are JSON with links to the workspace's logs, the resource's
  metrics or health page, the alert, the Activity Log or the cluster in
  the Azure portal, which become evidence links in the report. Lists are
  capped as [Azure source](sources/azure.md#limits) describes, and say
  when they were cut.
- **No data.** A query that returns no rows, metrics without values, an
  empty list, and a resource Azure does not have, are reported as no data.
- **Errors.** A refused request, such as a query Log Analytics cannot
  parse or a permission the identity lacks, goes to the model as a tool
  error with Azure's message. A refused token, rate limits, server errors
  and a credential that cannot be set up count as failures of the source.
- **Timeout.** `timeout` may be at most `60s`, because the HTTP client
  stops waiting for response headers after 60 seconds; `sidekick config
  validate` rejects a longer one.

#### AWS sources (`type: aws`)

An AWS source reads AWS accounts through CloudWatch Logs Insights,
CloudWatch metrics and alarms, CloudTrail event history, EKS, ECS and AWS
Health, with the default AWS credential chain (IRSA or EKS Pod Identity on
EKS) and, for other accounts, a role it assumes. Its
tools only read. [AWS source](sources/aws.md) lists the IAM policy the
role needs and how to set it up.

| Key | Meaning |
|---|---|
| `accounts` | The accounts the tools may read. Required; a call for any other account fails before anything is sent. Each has an `id` (12 digits; quote it in YAML) and, optionally, a `roleARN` in that account to assume and the `externalID` its trust policy asks for. At most one account may leave out `roleARN`: it is read with Sidekick's own credentials, which must belong to it. |
| `regions` | The Regions the tools may read, in every account. Required; a call for any other Region fails before anything is sent. |
| `logGroups` | The starts of the log group names `query_logs` may read, for example `/ecs/` or `/aws/eks/prod/`. Default: none, so `query_logs` reads nothing. |

```yaml
sources:
  aws:
    type: aws
    regions: [us-east-1, eu-west-1]
    logGroups: [/ecs/, /aws/eks/prod/]
    accounts:
      - id: "111122223333"
      - id: "444455556666"
        roleARN: arn:aws:iam::444455556666:role/sidekick-reader
        externalID: sidekick-prod
```

The tools, as the model sees them with the source name `aws`:

| Tool | Returns |
|---|---|
| `aws__query_logs` | The fields and rows of a Logs Insights query over named log groups or log group prefixes. |
| `aws__query_metrics` | CloudWatch metrics and metric math expressions, with a period that keeps each series within 120 points. |
| `aws__list_alarms` | CloudWatch alarms in a state, by default ALARM, or one alarm and its state changes. |
| `aws__list_changes` | CloudTrail write events: which API was called on which resources, by whom, and whether it failed. |
| `aws__get_eks_cluster` | An EKS cluster's status, version and health issues, and its node groups, or the list of clusters. |
| `aws__get_ecs_service` | An ECS service's task counts, deployments and latest events, or the cluster's services. |
| `aws__list_health_events` | AWS Health events of the Region and of global services. Needs a support plan that includes the Health API. |

How it behaves:

- **Start-up never waits for AWS.** The first tool call for an account
  reads the credentials, assumes its role or checks that Sidekick's own
  credentials belong to it. Assumed credentials are kept until shortly
  before they expire.
- **Scope.** Tools take `account` and `region`, which may be left out
  when the config lists one of each. `query_logs` takes log group names,
  or prefixes ending in `*`, that start with one of `logGroups`. Anything
  outside the config, or input that does not fit, such as a range longer
  than 7 days, is a tool error, and nothing is sent.
- **Queries.** The Logs Insights query goes to CloudWatch as the model
  wrote it, so the role's policy is what limits what can be read. A query
  with the `SOURCE` command, which picks its own log groups, is refused
  before it is sent. The query is started, polled every second and
  stopped when the call is about to run out of time.
- **Results** are JSON with links to the query in Logs Insights, the
  metrics graph, each alarm, each CloudTrail event, the cluster, the
  service and each Health event in the AWS console, which become evidence
  links in the report. Lists are capped as [AWS source](sources/aws.md#limits)
  describes, and say when they were cut.
- **No data.** A query that returns no rows, metrics without points, an
  empty list, and a log group, alarm, cluster or service AWS does not
  have, are reported as no data.
- **Errors.** A refused request, such as a query Logs Insights cannot
  parse or a permission the role lacks, goes to the model as a tool error
  with AWS's message. Rate limits, refused credentials, server errors and
  a role that cannot be assumed count as failures of the source.

#### Memory sources (`type: memory`)

A memory source lets the model search Sidekick's own memory: the past
investigations whose verdict a responder confirmed, the active
[lessons](#lessons) that match, and the notes people wrote about services.
It has only the keys every source has. [The memory
source](sources/memory.md) lists what its tools return.

```yaml
sources:
  memory:
    type: memory
```

The tools, as the model sees them with the source name `memory`, are
`memory__search` and `memory__service_notes`. Prompts show similar
confirmed investigations without it, as [`memory`](#memory) describes.

#### Prometheus sources (`type: prometheus`)

A Prometheus source reads Prometheus or a server that shares its HTTP API,
such as Thanos, Mimir, Cortex or VictoriaMetrics, and optionally an
Alertmanager, with a bearer token, basic auth or a client certificate from
Secrets, or none. Its tools only read. [Prometheus
source](sources/prometheus.md) lists every key, the access it needs and
its limits.

| Key | Meaning |
|---|---|
| `baseURL` | The root of the Prometheus HTTP API, the part before `/api/v1`. Required. It must use `https` when credentials are sent, except for `localhost` and loopback addresses. |
| `alertmanagerURL` | The root of an Alertmanager API, the part before `/api/v2`. When set, `list_alerts` reads Alertmanager, with silences. |
| `bearerTokenSecret` | `{ name, key }` of the Secret that holds a bearer token. |
| `basicAuth` | `username`, and `passwordSecret`, the `{ name, key }` of the Secret that holds the password. Not with `bearerTokenSecret`. |
| `tls` | `certSecret` and `keySecret` for a client certificate, and `caSecret` for the CAs to trust, each a `{ name, key }`. |
| `headers` | Extra headers for every request, such as `X-Scope-OrgID`. Never credentials. |
| `links` | `prometheusURL`, the web UI for evidence links (default: `baseURL`), or `grafanaURL` and `grafanaDatasource` for links to Grafana Explore. |

```yaml
sources:
  prometheus:
    type: prometheus
    baseURL: http://prometheus-operated.monitoring:9090
    alertmanagerURL: http://alertmanager-operated.monitoring:9093
```

The tools, as the model sees them with the source name `prometheus`:

| Tool | Returns |
|---|---|
| `prometheus__query` | An instant PromQL query: each series' labels and value. |
| `prometheus__query_range` | A PromQL range query with a step picked for at most 250 points per series: the min, max, average and last of each series, and its points. |
| `prometheus__list_alerts` | The firing and pending alerts, or the Alertmanager alerts with their silences. |
| `prometheus__get_rules` | The alerting rules with one name: expression, `for`, labels, health and last error. |
| `prometheus__list_series` | The series that match a series selector. |
| `prometheus__list_label_values` | The values of one label. |
| `prometheus__list_targets` | The scrape targets that are not up. |

How it behaves:

- **Start-up never waits for Prometheus.** The first tool call is the
  first request. Secrets are read again for each request, so a rotated
  one needs no restart.
- **Queries.** Every PromQL query and series selector is parsed with the
  Prometheus parser before it is sent; one that does not parse is a tool
  error, and nothing is sent.
- **Results** are JSON with links to the query in the Prometheus web UI or
  Grafana Explore, or to the alerts or targets page, which become evidence
  links in the report. Lists are capped as
  [Prometheus source](sources/prometheus.md#limits) describes, and say
  when they were cut.
- **No data.** A query with no series, an empty list, and an API the
  server does not have, are reported as no data.
- **Errors.** A query the server refuses or cannot run, a `403`, and an
  answer larger than 16 MiB go to the model as a tool error with the
  server's message. A refused credential, rate limits, server errors and
  secrets that cannot be read count as failures of the source.
- **Timeout.** `timeout` may be at most `60s`, because the HTTP client
  stops waiting for response headers after 60 seconds; `sidekick config
  validate` rejects a longer one.

#### Argo CD sources (`type: argocd`)

An Argo CD source reads the applications Argo CD deploys, with an API token:
their sync status, health, whether auto-sync is on, and their deployment
history, so investigations can see what was deployed, when and by whom. Its
tools only read. [The Argo CD source](sources/argocd.md) lists its limits and
the exact access the token needs, as an Argo CD RBAC policy.

| Key | Meaning |
|---|---|
| `baseURL` | The address of your Argo CD server, with its sub-path when it is served from one. Required. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses. |
| `tokenSecret` | `{ name, key }` of the Secret that holds the API token. Required. |
| `applications` | The applications the tools may read, as `project/name` globs such as `shop/*`, which is how Argo CD RBAC names them. Required; a call for any other application fails before anything is sent. |
| `namespace` | The namespace Argo CD runs in, and so the namespace of the applications the tools read. Default: `argocd`. Applications of other namespaces are not read. |

```yaml
sources:
  argocd:
    type: argocd
    baseURL: https://argocd.example.com
    tokenSecret: { name: sidekick-argocd, key: token }
    applications:
      - shop/*
      - platform/ingress-*
```

The tools, as the model sees them with the source name `argocd`:

| Tool | Returns |
|---|---|
| `argocd__list_applications` | The applications the config allows: project, destination, sync status, health, whether auto-sync is on, the revision deployed now and the one last compared, when it last synced, and a link. It can filter by project, name pattern and state (out of sync, not healthy). |
| `argocd__get_application` | One application: its source, destination, sync policy with a sentence that says whether auto-sync is on, status, last operation, conditions, images and up to 20 resources that are not synced or not healthy. |
| `argocd__get_history` | The deployment history of one application, newest first: revision, time, who started each deploy, and the commit message, author and date of each of the newest 10. Argo CD keeps as many entries as the application's `revisionHistoryLimit`, 10 by default. |

How it behaves:

- **Start-up never waits for Argo CD.** The token is read from its Secret
  for each request, so a rotated token needs no restart.
- **Scope.** Both tools that read one application take it as
  `project/name`. One that no `applications` glob allows, or a name without
  its project, is a tool error, and nothing is sent.
- **Results** are JSON with links to the applications' pages in the Argo CD
  UI, which become evidence links in the report. Lists are capped as
  [The Argo CD source](sources/argocd.md#limits) describes, and say when
  they were cut.
- **No data.** An application Argo CD does not have, a list without matches
  and an application without history are reported as no data.
- **Errors.** A refused request, such as a `403` for an application the
  token may not get, goes to the model as a tool error. A refused or expired
  token, rate limits and server errors count as failures of the source.
- **Timeout.** `timeout` may be at most `60s`, because the HTTP client
  stops waiting for response headers after 60 seconds; `sidekick config
  validate` rejects a longer one.

#### LaunchDarkly sources (`type: launchdarkly`)

A LaunchDarkly source reads feature flag changes from the LaunchDarkly
audit log and the current state of flags, with an access token, so
investigations can see that someone changed a flag. Its tools only read.
[The LaunchDarkly source](sources/launchdarkly.md) lists its limits and
how to make a token with the Reader role.

| Key | Meaning |
|---|---|
| `tokenSecret` | `{ name, key }` of the Secret that holds the access token. Required. |
| `projects` | The projects the tools may read, each with its `key` and the `environments` in it they may read. Required; a call for any other project or environment fails before anything is sent. |
| `baseURL` | The root of the LaunchDarkly instance: `https://app.eu.launchdarkly.com` for EU, `https://app.launchdarkly.us` for Federal. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses. Default: `https://app.launchdarkly.com`. |

```yaml
sources:
  flags:
    type: launchdarkly
    tokenSecret: { name: sidekick-launchdarkly, key: token }
    projects:
      - key: shop
        environments: [production, staging]
```

The tools, as the model sees them with the source name `flags`:

| Tool | Returns |
|---|---|
| `flags__list_flag_changes` | Audit log changes to the flags of one project and environment in a time range of at most 7 days, or to one flag: who, when, what was done, the summary, the comment and a link. |
| `flags__get_flag` | One flag's state in one environment: on or off, the number of rules and targets, what it serves by default and when off, and when it last changed. |

How it behaves:

- **Start-up never waits for LaunchDarkly.** The token is read from its
  Secret for each request, so a rotated token needs no restart.
- **Scope.** Both tools take `project` and `environment`, which may be
  left out when the config lists only one. Anything outside `projects`, a
  key that is not one, or a range longer than 7 days is a tool error, and
  nothing is sent.
- **Results** are JSON with links to the flags' pages in LaunchDarkly,
  which become evidence links in the report. Lists are capped as
  [The LaunchDarkly source](sources/launchdarkly.md#limits) describes, and
  say when they were cut.
- **No data.** A range without changes, and a project or flag LaunchDarkly
  does not have, are reported as no data.
- **Errors.** A refused request, such as a 400 or a 403, goes to the model
  as a tool error with LaunchDarkly's message. A refused token, rate
  limits and server errors count as failures of the source.
- **Timeout.** `timeout` may be at most `60s`, because the HTTP client
  stops waiting for response headers after 60 seconds; `sidekick config
  validate` rejects a longer one.

#### Unleash sources (`type: unleash`)

An Unleash source reads feature flag changes from the Unleash event log and
the current state of flags, with a service account token, so investigations
can see that someone changed a flag. Its tools only read.
[The Unleash source](sources/unleash.md) lists its limits and how to make a
token with the Viewer role.

| Key | Meaning |
|---|---|
| `baseURL` | The root of the Unleash instance, such as `https://unleash.example.com`. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses. Required. |
| `tokenSecret` | `{ name, key }` of the Secret that holds the service account token. Required. |
| `projects` | The projects the tools may read, each with its `key` (Unleash's default project is `default`) and the `environments` in it they may read. Required; a call for any other project or environment fails before anything is sent. |

```yaml
sources:
  flags:
    type: unleash
    baseURL: https://unleash.example.com
    tokenSecret: { name: sidekick-unleash, key: token }
    projects:
      - key: default
        environments: [production, development]
```

The tools, as the model sees them with the source name `flags`:

| Tool | Returns |
|---|---|
| `flags__list_flag_changes` | Events for the flags of one project and environment in a time range of at most 7 days, or for one flag: who, when, what was done, Unleash's summary and a link. |
| `flags__get_flag` | One flag's state in one environment: on or off, the number of strategies and dependencies, and when it last changed. |

How it behaves:

- **Start-up never waits for Unleash.** The token is read from its Secret
  for each request, so a rotated token needs no restart.
- **Scope.** Both tools take `project` and `environment`, which may be
  left out when the config lists only one. Anything outside `projects`, a
  key that is not one, or a range longer than 7 days is a tool error, and
  nothing is sent.
- **Results** are JSON with links to the flags' pages in the Unleash UI,
  which become evidence links in the report. Lists are capped as
  [The Unleash source](sources/unleash.md#limits) describes, and say when
  they were cut.
- **No data.** A range without changes, and a project or flag Unleash does
  not have, are reported as no data.
- **Errors.** A request Unleash refuses as invalid, such as a 400, goes to
  the model as a tool error with Unleash's message. A refused token (401),
  a token that may not read (403), rate limits and server errors count as
  failures of the source.
- **Timeout.** `timeout` may be at most `60s`, because the HTTP client
  stops waiting for response headers after 60 seconds; `sidekick config
  validate` rejects a longer one.

#### Mock sources (`type: mock`)

A mock source answers tool calls from fixtures instead of a real system.
Benchmark scenarios use it for every source; [Benchmarks](benchmark.md#mock-sources)
lists its keys: `tools.snapshot`, `fixtures`, `default` and `unavailable`.
It can also go under `sources` in `sidekick.yaml`, for example to try a route
without access to the real system.

### `intake.webhooks`

Each webhook endpoint receives alerts at `/hooks/<name>`. It names an
`adapter` that reads the sender's payload, and it must authenticate
deliveries with exactly one of `bearer` or `hmac`. Set `trusted: true` only
for senders you control: routes in `read-write` mode can select only trusted
endpoints (see [action modes](#action-modes)).

```yaml
intake:
  webhooks:
    alertmanager:
      adapter: alertmanager
      auth: { bearer: { name: sidekick-hooks, key: alertmanager } }
      trusted: true
    generic-ci:
      adapter: generic
      auth: { hmac: { name: sidekick-hooks, key: ci }, tolerance: 5m }
      fields:
        title: $.alert.title
        service: $.labels.service
```

`tolerance` is how far the signed timestamp may be from the time of arrival.

The adapters are `alertmanager`, which has no settings, and `generic`,
whose `fields` are JSON paths into the payload for `title` (required),
`text`, `service`, `severity`, `key` and `labels`. A path that does not
parse is reported when the file loads.
[Sending alerts to Sidekick](webhooks.md) describes both adapters, how
senders sign or authenticate deliveries, and what Sidekick answers.

The adapters `signoz`, `datadog`, `sentry` and `grafana` read the
webhooks of those tools. Only `grafana` has a setting, an optional
`signature` section for Grafana's own HMAC signature. `sentry` checks
Sentry's signature with the secret in `auth.hmac`. [Setting up
senders](senders.md) shows how to set up each one.

### `slack`

```yaml
slack:
  botTokenSecret: { name: sidekick-slack, key: botToken }   # required when the section is set
  appTokenSecret: { name: sidekick-slack, key: appToken }   # Socket Mode
  mode: socket                                              # or events
  signingSecret: { name: sidekick-slack, key: signingSecret }   # required with mode: events
  followUps: mention                                        # or any
  trustedBots: [B01SIGNOZ]                                  # bot IDs whose messages can be trusted
  apiURL: https://slack.com/api/                            # optional
  roleGroups:                                               # Slack user group ID to role
    S01ONCALL: responder
    S01SRE: admin
  roleUsers:                                                # Slack user ID to role
    U01LEAD: approver
```

`botTokenSecret` is the bot token of the Slack app. Every `slack` output
posts with it, so the section is required when an output has
`type: slack`.

The other keys let Sidekick receive messages, so that
[listeners](#listeners) and mentions of `@Sidekick` start
investigations:

- `mode: socket`, the default, connects to Slack over Socket Mode with the
  app-level token in `appTokenSecret`, so Sidekick needs no public URL.
  Without `appTokenSecret`, Sidekick only posts, and says so in a log line
  at start-up.
- `mode: events` receives events at `POST /slack/events` on
  `server.listen` instead, checked with the signing secret in
  `signingSecret`. Slack must be able to reach that URL.
- `followUps` says which replies in the thread of an investigation are
  follow-up questions: `mention`, the default, takes only replies that
  mention `@Sidekick`, and `any` takes every reply a person writes there.
  [Follow-up questions](slack.md#follow-up-questions) describes how they
  are answered.
- `trustedBots` lists the bot IDs (`B...`) whose messages are trusted in a
  listener marked `trusted: true`. Every other message is untrusted.
- `apiURL` is the Slack Web API base URL, ending in `/`. GovSlack
  workspaces use `https://slack-gov.com/api/`.
- `roleGroups` and `roleUsers` give Slack users their Sidekick role, one
  of `viewer`, `responder`, `approver` and `admin`. `roleGroups` maps
  Slack user group IDs (`S...`) to roles, and `roleUsers` maps user IDs
  (`U...` or `W...`) to roles, for teams without user groups. With the
  [`oidc` section](#oidc) set, a Slack user whose email a web UI user has
  also gets that user's role. A user gets the highest of these roles; a
  user none of them gives a role is a `responder`.
  [Roles](slack.md#roles) says what each role may do.

[Investigations in Slack](slack.md) describes how to set up the app.

### `teams`

```yaml
teams:
  appID: 00000000-1111-2222-3333-444444444444      # the bot's Microsoft App ID; required
  tenantID: 55555555-6666-7777-8888-999999999999   # required
  clientSecret: { name: sidekick-teams, key: clientSecret }   # or workloadIdentity: true
  workloadIdentity: false
  trustedBots: ["28:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]  # bots whose messages can be trusted
  serviceURL: https://smba.trafficmanager.net/teams/        # optional
```

The section sets up the Microsoft Teams bot, an Azure Bot resource of
type single tenant or user-assigned managed identity.

- `appID` is the bot's Microsoft App ID: the client ID of its Microsoft
  Entra app registration or managed identity.
- `tenantID` is the Microsoft Entra tenant of the bot and the teams it is
  in. Messages from any other tenant are refused.
- The bot gets its tokens in one of two ways; set exactly one:
  - `clientSecret` names a client secret of the app registration.
  - `workloadIdentity: true` uses Microsoft Entra Workload ID on AKS: the
    token file in `AZURE_FEDERATED_TOKEN_FILE`, with a federated credential
    for Sidekick's service account on the app registration or managed
    identity. [Cloud credentials](install.md#cloud-credentials) shows how
    to set it up with the chart.
- `trustedBots` lists the Teams IDs of bots (`28:` and the bot's app ID)
  whose messages are trusted in a listener marked `trusted: true`. Every
  other message is untrusted.
- `serviceURL` is the Bot Framework service URL for messages that do not
  answer a Teams message, such as the result of a webhook alert. It must
  be https and end in `/`.

With the section set, Sidekick serves the bot's messaging endpoint at
`POST /teams/messages` on `server.listen`, which needs a public https URL.
[Investigations in Microsoft Teams](teams.md) describes how to set up the
bot.

### `oidc`

```yaml
oidc:
  issuer: https://acme.okta.com                         # required
  clientID: sidekick                                    # required
  clientSecret: { name: sidekick-oidc, key: clientSecret }   # required
  groupsClaim: groups                                   # the ID token claim with the user's groups
  scopes: [groups]                                      # asked for besides openid, email and profile
  roles:                                                # role to groups; the highest match wins
    admin: [sre-admins]
    approver: [oncall-leads]
    responder: [engineering]
    viewer: ["*"]                                       # "*" matches everyone
```

The section turns on the web UI login and the JSON API under `/api/v1`.
Without it, neither is served. [Web UI and API](web-ui.md) says how to
register Sidekick with your identity provider.

- `issuer` is the provider's issuer URL. It must be https, except on a
  loopback address for local testing.
- `clientID` and `clientSecret` are those of the app you register for
  Sidekick at the provider. The redirect URI to register is
  `<server.publicURL>/auth/callback`, so `server.publicURL` is required
  and must be https.
- `groupsClaim` names the ID token claim that lists the user's groups.
  Some providers send groups only when a scope asks for them; list it in
  `scopes`.
- `roles` maps each role to the groups that give it. A user gets the
  highest role any of their groups gives, and the role is read again at
  every login. A user no group matches cannot sign in, so map `viewer` to
  `"*"` to let everyone in with read access. `"*"` is allowed only for
  `viewer`, so a typo cannot make every user an admin, and not at all
  with `https://accounts.google.com`, which anyone with a Google account
  can sign in to. Google sends no groups claim of its own; send groups
  in a custom claim and name it in `groupsClaim`.

### `listeners`

A listener picks the messages in one Slack or Teams channel that start an
investigation. The listener's name is the event's intake, so a route can
select its events with `match.intake`.

```yaml
listeners:
  alerts:
    channel: "#alerts"                  # a channel name or ID; required
    match:                              # every field that is set must match
      sender:
        kind: bot                       # human or bot
        names: [SigNoz]                 # bot names, compared without case
        bots: [B01SIGNOZ]               # bot IDs
        users: [U01ABCDEF]              # user IDs
      text: "(?i)alert|firing"          # a regular expression
    extract:                            # regular expressions with named groups
      - "Alert: (?P<alert>[A-Za-z0-9]+)"
      - "service=(?P<service>[a-z0-9-]+)"
      - "(?i)severity: (?P<severity>[a-z]+)"
      - "env=(?P<env>[a-z]+)"           # any other name sets a label
    recovery: "(?i)resolved|recovered"  # turns a message into a recovery event
    trusted: true                       # trust the bots in slack.trustedBots
```

When `names`, `bots` or `users` are set, the sender must be in one of
them. `text`, `extract` and `recovery` run on the message text with the
text of its attachments and blocks added, one part per line.
[Listeners and mentions](slack.md#listeners-and-mentions) says which
messages a listener sees and how events are built from them. Listeners
need the Slack app to receive events, so `slack.appTokenSecret` (or
`mode: events`) must be set. Listeners can also be added from Slack with
[`/sidekick listen`](slack.md#the-sidekick-command).

`source: teams` makes a listener watch a Teams channel instead, through
the bot in the [`teams`](#teams) section. Its keys are the same, with
Teams IDs:

```yaml
listeners:
  teams-alerts:
    source: teams                       # slack, the default, or teams
    channel: "19:abc123@thread.tacv2"   # the channel ID; required
    match:
      sender:
        kind: bot
        bots: ["28:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]   # bot IDs
        users: [ffffffff-0000-1111-2222-333333333333]       # Microsoft Entra object IDs
    trusted: true                       # trust the bots in teams.trustedBots
```

Mentions of the bot in Teams have the intake `teams-mention`, which no
webhook endpoint or listener can be named.

### `outputs`

Outputs are where investigations are shown. Each one has a `type`, and
each route lists the outputs its investigations go to in `outputs`.

```yaml
outputs:
  slack-thread: { type: slack, channel: "#alerts" }   # apiURL is optional
  linear-comment:
    type: linear.comment
    apiKeySecret: { name: sidekick-linear, key: apiKey }
    workspace: acme
    teams: [OPS]
  jira-comment:
    type: jira.comment
    url: https://acme.atlassian.net
    email: sidekick@acme.example
    apiTokenSecret: { name: sidekick-jira, key: apiToken }
    projects: [OPS]
  callback:
    type: webhook
    url: https://hooks.internal.example.com/sidekick
    hmacSecret: { name: sidekick-hooks, key: callback }
```

The types:

- `slack` posts one message per investigation and edits it as the
  investigation runs. [Investigations in Slack](slack.md) describes its
  keys, what it posts and how to set up the Slack app.
- `teams` does the same in a Microsoft Teams thread, as the bot of the
  [`teams`](#teams) section, which it needs. [Investigations in Microsoft
  Teams](teams.md#what-it-posts) describes its one key, `channel`.
- `linear.comment` and `jira.comment` post the result as a comment on the
  Linear or Jira issue the alert names. [Results on tickets](tickets.md)
  describes their keys and how they find the issue.
- `webhook` posts the final result and watcher events as JSON to `url`,
  signed with `hmacSecret` the way webhook intake checks deliveries.
  [Webhook callbacks](webhook-callbacks.md) describes the body, the
  headers and how to check them.

### `actions`

Actions are the changes Sidekick may make, such as opening a pull request
or commenting on a ticket. They are defined here once, with their
credentials and limits, and each route lists the ones it may use in its
`actions`. A route in `approved` mode lets the model propose one of its
actions. Nothing runs until a person with the approver role approves the
proposal in Slack or in the web UI.

```yaml
actions:
  proposalTTL: 24h              # how long a proposal waits for a decision; default 24h
  maxPerInvestigation: 5        # most proposals one investigation may make; default 5
  monitor-note:                 # the action's name: lower-case letters, digits and "-"
    type: note.create           # the action type; it decodes the other keys
    tokenSecret: { name: notes, key: token }
    limits: { boards: [ops] }   # what the action may touch
```

`proposalTTL` and `maxPerInvestigation` are the only keys that are not
action names. Which keys an action takes, and what its `limits` mean,
depend on its `type`. Credentials are `{ name, key }` references to
mounted secrets like everywhere else, and an action should have a
credential of its own, with no more rights than its limits allow. The
[action types](#action-types) are listed below; [Writing an
action](plugins/actions.md) describes how to add one.

How a proposal is handled:

- The model gives the action, its parameters and a reason. Sidekick checks
  the parameters against the action's schema and limits before it stores
  the proposal; a failed check goes back to the model as an error. The
  parameters are text from the model, so approvers see them exactly as they
  will run, with unusual characters spelled out.
- A proposal waits for `proposalTTL`, then expires and can no longer be
  approved.
- Only a person approves: someone signed in to the web UI, or a Slack user
  with the approver role. An API token can list the proposals and reject
  them, but an approval from a token is refused.
- Approving checks again that the route still lists the action and is not
  read-only. Running checks that and the action's limits once more. If the
  config changed in between so that the proposal is no longer allowed, it
  fails with the reason and does nothing; the model or a person must
  propose again.
- An approved proposal runs once, even if a worker crashes and the job is
  tried again: the action gets the same idempotency key each time.

#### Action types

##### `linear.comment` and `linear.issue`

`linear.comment` adds a comment to a Linear issue, and `linear.issue`
creates one. Both use the same connection keys. Their text is posted as
plain text: Sidekick escapes it, so a comment or description cannot add
formatting, links or mentions. Each ends with a line that says Sidekick
posted it and who approved it, an approver or the read + write policy of the
route, with a link to the investigation when [`server.publicURL`](#server)
is set.

```yaml
actions:
  ops-comment:
    type: linear.comment
    apiKeySecret: { name: linear-actions, key: apiKey }
    limits: { teams: [OPS] }
  ops-issue:
    type: linear.issue
    apiKeySecret: { name: linear-actions, key: apiKey }
    limits:
      teams: [OPS]              # team keys or IDs
      labels: [incident]        # the only labels an issue may get; none by default
      maxPerDay: 5              # most issues created in a day of budgets.daily.timeZone
```

| Key | Meaning |
|---|---|
| `apiKeySecret` | A Linear personal API key or an OAuth app token. Required. Use a key of its own, apart from any key an output or a source uses. A comment needs the right to read issues and create comments; an issue needs the right to create issues. |
| `apiURL` | The GraphQL endpoint; `https://api.linear.app/graphql` when empty. It must be https, except on localhost. |
| `limits.teams` | Required. The keys, such as `OPS`, or IDs of the teams the action may touch. A comment on an issue of another team, and an issue in another team, fail with the limit as the reason. |
| `limits.labels` | `linear.issue` only. The labels an issue may get. Empty means none. |
| `limits.maxPerDay` | `linear.issue` only. Required, at least 1. |

What the model gives: for `linear.comment`, the issue (`OPS-123`) and the
body (at most 4000 characters); for `linear.issue`, the team, a title (at
most 200 characters), a description (at most 4000) and up to 5 labels.

- A comment is checked against the team of the issue as Linear reports it
  when it runs, so an issue that moved to another team is refused.
- `maxPerDay` counts the issues this action created today, in the time
  zone of `budgets.daily.timeZone`, plus the ones approved earlier and not
  finished. It errs on the safe side: an issue that was approved earlier
  and then failed keeps its place for the runs that started before it
  ended. At the limit the proposal fails with the limit as the reason; the
  count starts again at midnight.
- A repeated run, after a crash, finds the comment or issue it made and
  makes no second one. Linear has no idempotency header, so the comment or
  issue gets an ID made from the proposal, and the description of an issue
  holds the line `Sidekick key: sidekick/<proposal id>`, which is the
  fallback way to find it.
- Routes that use these actions should set `mode: approved`.

##### `slack.reply` and `slack.reaction`

`slack.reply` posts a reply in the Slack thread of the investigation, and
`slack.reaction` adds an emoji to the Slack message that carried the alert.
They post as the bot of the [`slack`](#slack) section, which is required, and
need no credential of their own. The bot needs the scopes the
[Slack app manifest](slack.md) already asks for: `chat:write`,
`reactions:write`, and `channels:history` or `groups:history` to look for an
earlier reply. It must be a member of the channel.

```yaml
actions:
  thread-reply:
    type: slack.reply
    limits: { channels: [C0123456789] }
  ack-alert:
    type: slack.reaction
    limits:
      channels: [C0123456789]
      emoji: [eyes, white_check_mark]
```

| Key | Meaning |
|---|---|
| `limits.channels` | Required. The IDs of the channels the action may post in. The channel of the message the action finds must be one of them, or the proposal fails with the limit as the reason. |
| `limits.emoji` | `slack.reaction` only. Required. The names of the emoji it may add, without colons. |
| `apiURL` | The Web API base URL; `https://slack.com/api/` when empty. It must be https, except on localhost. |

The model gives only the text (`slack.reply`, at most 1500 characters) or the
emoji (`slack.reaction`). It never gives a channel or a message, because it
does not see them; Sidekick finds them:

- The reply goes in the thread where the investigation's report is, which is
  the thread of its first Slack output. When the investigation has none, it
  goes in the thread of the alert message. The proposal fails if the
  investigation has neither.
- The reaction goes on the alert message: the first message of the
  investigation's group that is an alert or a person's message in a listener's
  channel. A later mention of Sidekick in the same group does not count. The
  proposal fails if the alert did not come from Slack, for example because it
  came in through a webhook.

The text is posted as plain text. Slack never reads it as markup, so it
cannot mention anyone or add a link; the line under it, which says Sidekick
sent it and whether an approver or the read + write policy of the route
approved it, and links to the investigation when
[`server.publicURL`](#server) is set, is the only markup. Routes that use these actions should set
`mode: approved`.

A repeated run, after a crash, makes one reply and one reaction. A reply
carries the idempotency key in its Slack message metadata, and a repeat looks
for it among the bot's newest messages in the thread before it posts. A
reaction that is there already counts as done. If Slack does not return the
metadata of the bot's own messages, a repeat after a lost answer could post
the reply twice; nothing else is affected.

##### `jira.comment` and `jira.issue`

`jira.comment` adds a comment to a Jira issue, and `jira.issue` creates one,
in a Jira Cloud site through the REST API with an email and an API token.
Jira Data Center is not supported; a `bearerTokenSecret` key is refused with
a message that says so. The text is written as plain text nodes, so it
cannot add formatting, links or mentions. Each comment and description ends
with a line that says Sidekick posted it and whether an approver or the
read + write policy of the route approved it, with a link to the
investigation when [`server.publicURL`](#server) is set.

```yaml
actions:
  ops-comment:
    type: jira.comment
    url: https://acme.atlassian.net
    email: sidekick-actions@acme.example.com
    apiTokenSecret: { name: jira-actions, key: apiToken }
    limits: { projects: [OPS] }
  ops-issue:
    type: jira.issue
    url: https://acme.atlassian.net
    email: sidekick-actions@acme.example.com
    apiTokenSecret: { name: jira-actions, key: apiToken }
    limits:
      projects: [OPS]
      issueTypes: [Task, Bug]
      labels: [incident]        # the only labels an issue may get; none by default
      maxPerDay: 5              # most issues created in a day of budgets.daily.timeZone
```

| Key | Meaning |
|---|---|
| `url` | The address of the Jira Cloud site. Required. It must be https, except on localhost. |
| `email`, `apiTokenSecret` | The account and its API token. Required. Use an account and a token of their own, apart from any the `jira.comment` output uses. The account needs to browse and add comments in the projects below; for `jira.issue` it also needs to create issues in them. |
| `limits.projects` | Required. The keys of the projects the action may touch. A comment on an issue of another project, and an issue in another project, fail with the limit as the reason. |
| `limits.issueTypes` | `jira.issue` only. Required. The issue types it may create, by name. |
| `limits.labels` | `jira.issue` only. The labels an issue may get, spelled as in Jira. Empty means none. |
| `limits.maxPerDay` | `jira.issue` only. Required, at least 1. |

What the model gives: for `jira.comment`, the issue (`OPS-123`) and the body
(at most 4000 characters); for `jira.issue`, the project, the issue type, a
one-line summary (at most 200 characters), a description (at most 4000) and
up to 5 labels.

- A comment on an issue given by its numeric ID, or whose key now belongs to
  another project, is checked against the project Jira reports when it runs.
- `maxPerDay` counts the issues this action created today, in the time zone
  of `budgets.daily.timeZone`, plus the ones approved earlier and not
  finished. It errs on the safe side: an issue that was approved earlier and
  then failed keeps its place for the runs that started before it ended. At
  the limit the proposal fails with the limit as the reason; the count
  starts again at midnight.
- A repeated run, after a crash, makes no second comment or issue. A comment
  ends with a line `sidekick:<hash>`, which a repeat looks for among the
  newest 50 comments. An issue gets a label `sidekick-<hash>`, which a repeat
  looks for with a search of the project; Jira's search can lag by a few
  seconds behind a new issue, so a repeat right after a lost answer may not
  see it yet. The label shows on the issue.
- Routes that use these actions should set `mode: approved`.

##### `datadog.monitor`

Changes a Datadog monitor in place through the Datadog API, or deletes it.
Use it on routes with `mode: approved`: the change goes live as soon as it
is approved, and there is no review in Git.

```yaml
actions:
  datadog-monitor:
    type: datadog.monitor
    site: datadoghq.com                # default; the API is at api.<site>
    apiKeySecret: { name: sidekick-datadog-write, key: apiKey }
    appKeySecret: { name: sidekick-datadog-write, key: appKey }
    limits:
      monitorIds: [12345]              # monitors it may change ...
      monitorTags: ["team:payments"]   # ... and monitors that have one of these tags
      fields: [options.thresholds, options.renotify_interval, message, tags, priority]
      allowDelete: false               # default false
```

- `site` is the Datadog site, such as `datadoghq.eu` or `us3.datadoghq.com`.
  `url` replaces it with the full address of the API, for a proxy; it must
  be https, except for a loopback host. They cannot be set together.
- `apiKeySecret` and `appKeySecret` are the API key and the application key
  of a service account that has the Monitors Read and Monitors Write
  permissions and no others. Use keys of their own, apart from the keys of
  any Datadog source. Both are read on every call, so a rotated key needs no
  restart.
- `limits.monitorIds` and `limits.monitorTags` say which monitors it may
  change: at least one of them is required. A monitor is allowed when its
  ID is listed, or when it has one of the tags.
- `limits.fields` lists the fields it may change, as paths into the monitor
  such as `options.thresholds.critical`, `message` or `tags`. A path allows
  the fields below it too, so `options.thresholds` allows
  `options.thresholds.critical`, but `message` does not allow `messages`.
  The first key must be `name`, `query`, `message`, `tags`, `priority` or
  `options`. Positions in a list are written `[0]`, and `[*]` stands for any
  position.
  Allowing `message` lets the model change which handles are notified,
  because the `@` handles in a Datadog message (such as `@pagerduty-team` or
  `@slack-ops`) decide where notifications go. Approvers should read the
  handles in the difference. Allowing `tags` lets the model change which
  monitors the action reaches by tag; a change that would remove the tag
  that allows a monitor is refused, so the action never moves a monitor out of
  its own limits.
- A `set` value of `null` removes an option: Datadog reads a null option
  back as one that is not there. Datadog keeps `options.no_data_timeframe`
  only while `options.notify_no_data` is true, so a change that turns
  `notify_no_data` off must set `no_data_timeframe` to `null` in the same
  change, and a change that sets the timeframe must set `notify_no_data` to
  true in the same change. Sidekick refuses the others, with a message that
  says so, so that Undo can put the timeframe back.
- `allowDelete: true` lets the model propose to delete a monitor. A delete
  cannot be undone in Sidekick, and Slack and the web UI say so before it is
  approved.

The model proposes `monitorId` and `set`, a map from field path to new value, or
`delete: true`. When it proposes, Sidekick reads the monitor, computes the
new definition, refuses any change outside the limits, and asks Datadog to
check the new definition (`POST /api/v1/monitor/{id}/validate`) before the
proposal is shown. Approvers see the change field by field. Right before it
writes, Sidekick reads the monitor again and fails the proposal when the
monitor was changed in Datadog since, so a new proposal is needed.

An approver can undo a change that worked. Undo puts back only the fields
Sidekick changed and leaves every other field as it is. If someone changed one
of those fields since, Undo shows what a restore would overwrite and asks
before it restores it.

##### `datadog.downtime`

Mutes the notifications of a Datadog monitor for a limited time, for planned
work or a known noisy period, by making a downtime. A downtime is not a
change to the monitor: the monitor keeps evaluating and keeps its state, and
Datadog only holds back its notifications. Use it on routes with
`mode: approved`: the downtime starts as soon as it is approved. It needs no
`allowReadWrite`.

```yaml
actions:
  mute-payments:
    type: datadog.downtime
    site: datadoghq.com                # default; the API is at api.<site>
    apiKeySecret: { name: sidekick-datadog-downtime, key: apiKey }
    appKeySecret: { name: sidekick-datadog-downtime, key: appKey }
    limits:
      monitorIds: [12345]              # monitors it may mute ...
      monitorTags: ["team:payments"]   # ... and monitors that have one of these tags
      scopes:                          # at least one, written exactly as Datadog reads them
        - "env:prod"
        - "env:staging AND service:checkout"
      maxDuration: 2h                  # default 2h, at most 24h

routes:
  - name: payments-alerts
    match: { intake: [datadog], service: "payments-*" }
    mode: approved
    actions: [mute-payments]
```

- `site` is the Datadog site, such as `datadoghq.eu` or `us3.datadoghq.com`.
  `url` replaces it with the full address of the API, for a proxy; it must
  be https, except for a loopback host. They cannot be set together.
- `apiKeySecret` and `appKeySecret` are the API key and the application key
  of a service account. Use keys of their own, apart from the keys of any
  Datadog source and of `datadog.monitor`. Both are read on every call, so a
  rotated key needs no restart.
- `limits.monitorIds` and `limits.monitorTags` say which monitors it may
  mute: at least one of them is required. A monitor is allowed when its ID is
  listed, or when it has one of the tags. A tag may not be `*`, which would
  mute every monitor.
- `limits.scopes` lists the downtime scopes the model may use, and is
  required. A Datadog scope is a query, such as `env:prod` or
  `env:staging AND service:checkout`, so a scope the model proposes must be
  one of these, character for character; it is never matched by a pattern.
  `*` mutes every group of a monitor, and is allowed only when it is listed.
- `limits.maxDuration` is the longest downtime: from `5m` to `24h`, `2h` when
  not set. The shortest downtime is 5 minutes.

**Permissions.** The service account needs the Monitors Downtime
(`monitors_downtime`) and Monitors Read (`monitors_read`) permissions, and no
others. If you use application keys with scopes, give the application key
the scopes `monitors_downtime` and `monitors_read`. The downtime calls need
`monitors_downtime`, and Sidekick reads a monitor with `monitors_read`. The
Monitors Write permission is not needed, because a downtime does not change
the monitor. Datadog describes `monitors_downtime` as the permission to set
downtimes for any monitor in the organization, so the limits of this action
are the only thing that keeps the model to the monitors and scopes you chose.

**The rule for a downtime.** The model proposes exactly one of `monitorId` (an
integer) and `monitorTag` (one of `limits.monitorTags`, exactly), a `scope`, a
`duration` such as `30m` or `2h`, and a `message` that says why. A monitor
that is not listed in `monitorIds` is read, and is allowed only if it has one
of the tags; the check is made again right before the downtime is made. A
downtime by tag mutes every monitor that has the tag. The message is at most 500 characters, and line breaks and tabs
become spaces; text with other control characters is refused. The message
may not hold an `@`, because Datadog notifies the handle after an `@` in the
message of a downtime (a person, a channel or a webhook), and the model must
not choose who is notified. It may not hold the text `[sidekick proposal`
either, which Sidekick keeps for its marker. A downtime is one-time: there is
no recurrence.

**What the approver sees.** The proposal shows the downtime as it will be
made: the monitor or the tag, the scope, the duration and the message. For a
monitor it shows its name, type and current state. For a tag it shows how
many monitors have the tag (at least 100 when the answer is full) and the
first five names. That is information and not a limit. The downtime starts
when it is approved, not when it was proposed, and lasts the duration the
approver read.

**The marker.** Datadog has no way to recognise a repeated request, so
Sidekick adds a line to the message, `[sidekick proposal <id>]`. It shows
people who made the downtime, and a retry after a crash and Undo find the
downtime by it, by reading the whole list of downtimes, since Datadog's list
cannot be filtered by message. The line is found again when the message ends
with it, after a line break or any other white space. Before it makes a
downtime, Sidekick looks for one with the marker, in any state the list
shows, so a crash makes one downtime, and a downtime that a person canceled
in the meantime is not made again. Do not remove the line when you edit a
downtime in Datadog: Undo could not find it then, and would report that the
downtime is gone while it keeps running.

**Undo.** An approver can cancel a downtime early. Undo cancels the downtime
that has the marker, with `DELETE /api/v2/downtime/{id}`, which cancels it:
Datadog keeps a canceled downtime for about two days with the status
`canceled`. A downtime that has ended or was canceled already counts as
success. If someone edited the downtime since, so that its scope, its monitor
or tags, or its length are not what was written, or it became recurring,
Undo cancels nothing and shows the downtime as it is now; the approver then
confirms to cancel it anyway. Undo does not check the limits again, because
canceling a downtime is the safe direction.

**What it relies on.** The calls and fields come from the public API
specification of the `datadog-api-client-go` release v2.67.0, and the
comments in `internal/action/datadogdowntime` name the files. These points
are not stated there or in Datadog's documentation, and the action does not
depend on them for safety:

- What happens to a downtime when its monitor is deleted. Undo and the search
  for the marker never read the monitor.
- Whether the list of downtimes keeps showing canceled and ended downtimes,
  and for how long. A downtime that a person canceled is not made again only
  while Datadog still lists it.
- Whether Datadog keeps a line break in the message. If it does not, the
  marker is still found, as long as white space of some kind comes before it.
- Whether the list shows a downtime right after it was made. A retry within a
  moment of a lost answer might not see it and could make a second one.

##### `grafana.alert_rule`

Changes a Grafana-managed alert rule in place through Grafana's alerting
provisioning API, or deletes it. Use it on routes with `mode: approved`: the
change goes live as soon as it is approved, and there is no review in Git.
It supports Grafana 11.6 and later.

```yaml
actions:
  grafana-alert-rule:
    type: grafana.alert_rule
    url: https://acme.grafana.net      # the root of Grafana; https, or http on localhost
    tokenSecret: { name: sidekick-grafana-write, key: token }
    limits:
      folderUids: [payments-alerts]    # required: the folders whose rules it may change
      ruleUids: [a1b2c3d4]             # optional: only these rules ...
      titlePatterns: ["Checkout *"]    # ... and rules whose title matches one of these
      fields: [for, noDataState, "data[*].model.conditions[*].evaluator.params"]
      allowDelete: false               # default false
```

- `url` is the root of Grafana, such as `https://acme.grafana.net`. If
  Grafana is served from a sub-path, include it. It must be https, except
  for a loopback host.
- `tokenSecret` is a Grafana service account token. Use a token of its own,
  apart from the token of any Grafana source. It is read on every call, so
  a rotated token needs no restart. The permissions it needs are below.
- `limits.folderUids` is required. The action only touches rules in these
  folders. A folder's UID is in its address in Grafana, as in
  `/dashboards/f/<uid>/`.
- `limits.ruleUids` and `limits.titlePatterns` narrow the folders. With
  neither, every rule in the folders is allowed. With either, a rule is
  allowed when its UID is listed or its title matches a pattern. In a
  pattern `*` stands for any text and every other character stands for
  itself; a pattern matches the whole title, and case counts. A change that
  would give a rule a title that no pattern matches is refused, unless the
  rule's UID is listed, so the action never moves a rule out of its own
  limits.
- `limits.fields` lists the fields it may change, as paths into the rule,
  such as `for`, `labels.severity` or `data[1].model.expr`. A path allows
  the fields below it too. The first key must be `title`, `condition`,
  `data`, `noDataState`, `execErrState`, `for`, `keep_firing_for`,
  `annotations`, `labels` or `isPaused`. Positions in a list are written
  `[0]`, and `[*]` stands for any position. The folder and the rule group
  cannot be changed, so the action never moves a rule, and the notification
  settings of a rule are not part of the action.
- Name narrow paths. `data` is the list of queries and expressions of the
  rule, and it includes each query's `datasourceUid`, so allowing `data`
  lets the model point a query at another data source that the token can
  query, and rewrite any query. Allow `data` as a whole only when you accept
  that; otherwise name what you want changed, such as the threshold
  (`data[*].model.conditions[*].evaluator.params`) or the expression
  (`data[*].model.expr`). Allowing `annotations` or `labels` lets the model
  change the text people read in a notification and the labels that
  notification policies route on, which can change who is notified.
- A `set` value of `null` removes a label or an annotation. To change a
  label whose name has a dot, set `labels` as a whole object.
- `allowDelete: true` lets the model propose to delete a rule. A delete
  cannot be undone in Sidekick, and Slack and the web UI say so before it is
  approved.

The model proposes `ruleUid` and `set`, a map from field path to new value,
or `delete: true`. When it proposes, Sidekick reads the rule, computes the
new one, and refuses any change outside the limits. Grafana has no call that
checks a rule without storing it, so Sidekick checks the common problems
itself: the values of `noDataState` and `execErrState`, durations such as
`5m`, the title, the shape of each query, and a condition that names a query.
It also refuses a change that Grafana would silently replace, such as a
`maxDataPoints` of 0. Grafana can still refuse a rule when it is approved;
the proposal then fails with Grafana's message and nothing is stored. Right
before it writes, Sidekick reads the rule again and fails the proposal when
the rule was changed in Grafana since, so a new proposal is needed. Grafana
offers no condition on the write itself, so a change made in the moments
between that last read and the write is not noticed.

An approver can undo a change that worked. Undo puts back only the fields
Sidekick changed and leaves every other field as it is. If someone changed one
of those fields since, Undo shows what a restore would overwrite and asks
before it restores it.

What the action relies on from Grafana, as its source code shows (checked at
v13.2.5, v12.4.14 and v11.6.9 of
[grafana/grafana](https://github.com/grafana/grafana)):

- **Rules made in the UI stay editable in the UI.** Without the
  `X-Disable-Provenance` header, a rule written through the provisioning API
  is marked as provisioned and Grafana locks it in the UI. Every write and
  delete of this action sends the header.
- **Rules that something else manages are left alone.** A rule made from
  files, by Terraform or by another API client has a provenance. With the
  header Grafana refuses to update it, and without the header the action
  would take the rule away from its owner, so the action refuses to change
  or delete such a rule. Only the owner can allow Sidekick to change it, by
  removing the provisioning in Grafana, for example by managing the rule in
  the Grafana UI instead of in files or Terraform. Recording rules are not
  supported either.
- **The whole rule is written.** A write replaces the rule, so Sidekick
  sends every field it read, except the ones Grafana sets itself (`id`,
  `orgID`, `uid`, `updated` and `provenance`). The conflict check compares
  the rule with the snapshot and uses the `updated` time that Grafana sets
  on every write.
- **Grafana does not keep a rule exactly as sent.** It writes durations in
  their short form (`300s` reads back as `5m`), fills in `maxDataPoints`
  and `intervalMs` in the model of each query, sets the model's `refId`,
  and leaves empty `labels` and `annotations` out. Sidekick compares rules
  the way Grafana reads them back, so a retry after a crash and Undo see a
  change that is in place.
- **The provisioning routes are deprecated from Grafana 13.0.** They still
  work in 13.3.0 and answer with a deprecation warning. Grafana names the
  alerting resource API (`rules.alerting.grafana.app`) as the replacement,
  which this action does not use yet.

Permissions of the token. Create a service account for Sidekick and give it
only this:

- On Grafana Enterprise and Grafana Cloud, with a custom role, scoped to the
  folders in `limits.folderUids` (`folders:uid:<uid>`): `alert.rules:read`,
  `alert.rules:write` and `folders:read`; and `alert.rules:delete` only if
  `allowDelete` is true. Also `alert.provisioning.provenance:write`, which
  Grafana asks for on every write through this API, and has no scope.
  When the action may change `data`, also `datasources:query` on the data
  sources that those rules query, because Grafana checks that for a change of
  a query.
- On open source Grafana there are no custom roles, and
  `alert.provisioning.provenance:write` comes only with the Editor and Admin
  roles. The service account needs the Editor role, which can edit alert
  rules in every folder. Then `limits.folderUids` is the only thing that
  keeps the action in its folders, and the token should be kept as secret as
  any other credential that can edit all alert rules.

##### `signoz.alert_rule`

Changes an alert rule made in the SigNoz UI through the SigNoz API, or
deletes it. Use it on routes with `mode: approved`: the change goes live as
soon as it is approved, and there is no review in Git. A rule that
Terraform or another tool manages is not marked as such in SigNoz, so keep
those rules out of `limits`, or the next apply undoes the change.

```yaml
actions:
  signoz-alert-rule:
    type: signoz.alert_rule
    url: https://example.signoz.cloud   # the address of SigNoz; https, or http on a loopback host
    apiKeySecret: { name: sidekick-signoz-write, key: apiKey }
    apiKeyHeader: SIGNOZ-API-KEY        # default; only if the SigNoz server renamed it
    limits:
      ruleIds: ["0196a1b2-7c3d-7e4f-8a5b-1c2d3e4f5a6b"]   # rules it may change ...
      ruleNames: ["checkout *"]                           # ... and rules whose name matches
      fields: [condition.target, condition.thresholds, evalWindow, evaluation, labels]
      allowDelete: false                # default false

routes:
  - name: checkout-alerts
    match: { service: "checkout-*" }
    sources: [signoz]
    mode: approved
    actions: [signoz-alert-rule]
```

- `url` is the root of your SigNoz, such as `https://example.signoz.cloud`
  or the address of a self-hosted install. A path is kept, for a SigNoz
  behind a proxy.
- `apiKeySecret` holds an API key. In SigNoz, create a service account for
  Sidekick actions (**Settings**, **Service Accounts**), give it the managed
  `signoz-editor` role, which is the least role that can edit alert rules,
  and generate a key on its **Keys** tab. Use a key of its own, apart from the
  read-only key of the [SigNoz source](sources/signoz.md), so that the
  source cannot write. The key is read on every call, so a rotated key needs
  no restart. It is sent in the `SIGNOZ-API-KEY` header, or in the header
  `apiKeyHeader` names.
- `limits.ruleIds` and `limits.ruleNames` say which rules it may change: at
  least one of them is required. A rule is allowed when its ID is listed
  (the ID is in the address of the rule's page, `ruleId=...`), or when its
  name matches a pattern. In a pattern `*` is the only wildcard, the pattern
  must match the whole name, and capitals count. A change that would rename
  a rule so that it no longer matches is refused, unless the rule's ID is
  listed.
- `limits.fields` lists the fields it may change, as paths into the rule as
  SigNoz reads it. A path allows the fields below it too. These are the
  fields Sidekick can change; the others are refused when the config is
  checked:
  - `alert` (the name), `description`, `disabled`, `labels.<name>` and
    `annotations.<name>`, in every rule. A label or annotation whose name
    has a dot cannot be changed, because a dot in a path separates keys.
  - `condition.alertOnAbsent`, `condition.absentFor`,
    `condition.requireMinPoints` and `condition.requiredNumPoints`.
  - The filter and the query text:
    `condition.compositeQuery.queries[n].spec.filter.expression` and
    `.spec.having.expression` for builder queries, and `.spec.query` for
    PromQL and ClickHouse SQL queries.
  - In a rule without `schemaVersion` (SigNoz's older shape):
    `condition.target`, `condition.op`, `condition.matchType`,
    `condition.targetUnit`, `evalWindow`, `frequency` and
    `preferredChannels`.
  - In a rule with `schemaVersion: v2alpha1`:
    `condition.thresholds.spec[n]` with `name`, `target`, `targetUnit`,
    `recoveryTarget`, `matchType`, `op` and `channels`;
    `evaluation.spec.evalWindow` (rolling evaluations only) and
    `evaluation.spec.frequency`; and `notificationSettings` with `groupBy`,
    `usePolicy`, `newGroupEvalDelay`, `renotify.enabled`, `renotify.interval`
    and `renotify.alertStates`.
  A field of the other shape is refused, because SigNoz would keep the
  value and ignore it. Adding or removing a threshold or a query is not
  supported. Allowing `preferredChannels`, `channels` or
  `notificationSettings` lets the model change where notifications go;
  approvers should read those lines in the difference.
- A `set` value of `null` removes a label, an annotation, a description, a
  recovery target or another optional field. A duration is text such as
  `5m`; a number would mean nanoseconds and is refused.
- `allowDelete: true` lets the model propose to delete a rule. A delete
  cannot be undone in Sidekick, and Slack and the web UI say so before it is
  approved. SigNoz refuses to delete a rule that a planned maintenance still
  uses.

The model proposes `ruleId` and `set`, a map from field path to new value,
or `delete: true`. When it proposes, Sidekick reads the rule, computes the
new rule, and refuses any change outside the limits, any change SigNoz would
drop or ignore, and any value of the wrong form (for example text for a
threshold). SigNoz has no call that checks a rule without side effects: its
test endpoint sends a real notification, so Sidekick never calls it. SigNoz
checks the rule again when it is written, for the parts Sidekick cannot
check (the syntax of a label or annotation template, of a filter or of a
query, and whether a channel exists), and refuses a bad rule before it
stores anything. Then the proposal fails with SigNoz's message and the rule
is as it was. Approvers see the change field by field. Right before it
writes, Sidekick reads the rule again and fails the proposal when the rule
changed in SigNoz since, so a new proposal is needed. SigNoz does not offer a
way to make the write conditional, so a change in the moments between that
read and the write is not noticed.

Sidekick writes the whole rule, because SigNoz replaces the stored rule with
what it is sent. After the write it reads the rule again, and if SigNoz kept
another value than the one written, the proposal fails with a message that
says the rule was written, names the field, and links to the rule. An
approver can undo a change that worked. Undo puts back only the fields
Sidekick changed and leaves every other field as it is. If someone changed
one of those fields since, Undo shows what a restore would overwrite and
asks before it restores it.

Sidekick relies on this about SigNoz, which it checked in the SigNoz source
code at v0.134.0 and v0.148.1:

- It uses `GET`, `PUT` and `DELETE` on `/api/v2/rules/{id}`, and
  `GET /api/v1/version`. It supports SigNoz v0.134.0 and later and refuses an
  older one with an error. A version text that is not a release number, such
  as a custom build, is allowed with a warning in the log; the read after
  each write then protects the rule.
- The rule's `updatedAt` changes on every write, and Sidekick uses it, with
  the rule's fields, to find a change made since the proposal.
- SigNoz keeps the body of a write as it is, and reads it back through typed
  values: it lower-cases `op` and `matchType`, drops empty optional values,
  and drops keys it does not know. Sidekick compares values the way SigNoz
  reads them back.
- SigNoz shows a rule as disabled when the rule is not loaded in its rule
  manager, for example right after a restart, whatever the stored value is,
  and no read tells that from a rule a person disabled. Writing the whole rule
  back would store `disabled: true` and keep the rule off after SigNoz
  recovers. So Sidekick refuses to change a rule that reads as disabled, when
  it proposes, when it writes and when it undoes, unless the change itself
  sets `disabled` to `false`, which the approver then sees. A rule can be
  deleted while it reads as disabled, and an enabled rule can be disabled.
- Before v0.141.0 SigNoz checks channel names against the channel's name;
  from v0.141.0 it checks the display name shown in the UI.

##### `prometheus.rule`

Changes alerting and recording rules in a `PrometheusRule` (Prometheus
Operator) through the Kubernetes API of the cluster Sidekick runs in. Use it
on routes with `mode: approved`: the change goes live as soon as it is
approved, and there is no review in Git. If Argo CD, Flux or Helm own the
resource, they undo a live change at their next sync; use
[`github.pull_request`](#githubpull_request) for those.

```yaml
actions:
  prom-rule:
    type: prometheus.rule
    serviceAccount: sidekick-actions   # default; the account the tokens are for
    limits:
      namespaces: [monitoring]         # namespaces it may write in
      names: [checkout-alerts]         # PrometheusRule names, exact
      groups: [checkout.rules]         # rule groups, exact
      fields: [expr, for, keep_firing_for, labels, annotations]

routes:
  - name: checkout
    mode: approved
    actions: [prom-rule]
```

- `limits.namespaces`, `limits.names` and `limits.groups` are exact names,
  and each needs at least one. A PrometheusRule is allowed when its namespace
  is in `namespaces` and its name is in `names`; a rule is allowed when its
  group is in `groups`. There are no patterns, because the Roles that the
  chart makes list the names as `resourceNames`.
- `limits.fields` lists the fields of a rule it may change, as paths into the
  rule such as `for`, `labels.severity` or `annotations.summary`. A path
  allows the fields below it too. The first key must be `expr`, `for`,
  `keep_firing_for`, `labels` or `annotations`. The name of the alert or of
  the recording rule, and the group, cannot change.
  Allowing `labels` lets the model change how alerts are routed, because
  Alertmanager routes by label (`severity`, `team`). Allowing `expr` lets it
  silence an alert by changing what it measures. Approvers should read the
  difference.
- `serviceAccount` is the name of a service account in Sidekick's own
  namespace. The action asks the API server for a token for it
  (`TokenRequest`) each time it proposes, runs or undoes a change, and the
  token lives 10 minutes. Sidekick's own account can only ask for tokens of
  this account, and this account can only `get` and `patch` the
  PrometheusRules of the limits. Sidekick must mount its service account
  token, which the chart does when it finds such an action.
- The action works on the cluster Sidekick runs in only. Other clusters are
  not supported.
- Rules are named by group and by `alert` or `record`, never by position.
  The model proposes `namespace`, `name` and `rules`, a list of
  `{group, alert or record, set}`, where `set` maps a field of the rule to its
  new value. A `null` value removes the field. Setting `labels` whole
  replaces all labels, which is also how to set a label whose name has a dot.
  Rules cannot be added, removed or renamed, and the whole resource cannot be
  deleted: the Role has no `delete`. Two rules of a group with the same alert
  name cannot be addressed, and the action refuses them.
- A change of more than 100 fields is refused with a message to split it.

**What the chart creates.** For each `prometheus.rule` action in `config`
(turn this off with `rbac.actions: false` and make these yourself), the chart
makes the service account, a Role in Sidekick's namespace that lets Sidekick's
own account create tokens for it, and in each listed namespace a Role and a
RoleBinding. The rules of the two kinds of Role are:

```yaml
# In each namespace of limits.namespaces:
rules:
  - apiGroups: [monitoring.coreos.com]
    resources: [prometheusrules]
    resourceNames: [checkout-alerts]
    verbs: [get, patch]
---
# In Sidekick's namespace, for Sidekick's own account:
rules:
  - apiGroups: [""]
    resources: [serviceaccounts/token]
    resourceNames: [sidekick-actions]
    verbs: [create]
```

**What it checks.** When the model proposes, Sidekick reads the resource,
works out the new rules and refuses what is outside the limits, a field of the
wrong type, or a rule that is not there (the error lists the rules of the
allowed groups). It then has the [validator](validator.md), which needs
`validator.enabled: true` in the chart, check the whole rule groups it changes
with `promtool check rules`, and asks the API server for
a dry run of the patch. The rules in the answer of the dry run must be the
rules Sidekick meant, so a change the server would drop (for example
`keep_firing_for` on an Operator version without that field) is refused
before anyone approves it. Approvers see the change field by field. When the
change is approved, Sidekick reads the resource again, and runs the validator and
the dry run again before it writes.

**Conflicts.** The patch holds `metadata.resourceVersion` from the read right
before it, so the API server answers 409 if the resource changed between the
read and the write, and the proposal fails as a conflict. The version at the
time of the proposal is recorded but not compared: the Operator can write the
status of a PrometheusRule, and other people edit other rules of the same
resource, and neither is a reason to fail. If the rules the change names differ
from what the approver read, the proposal fails and a new one is needed.

**Undo.** Undo puts back only the fields Sidekick changed and leaves every
other field as it is. If someone changed one of those fields since, Undo shows
what a restore would overwrite and asks before it restores it. Undo checks the
rules and dry-runs the patch the same way. An `expr` that was a number is
written back as text, which the CRD allows.

**What it relies on.** The patch is a JSON merge patch that sends all of
`spec.groups` (a merge patch replaces a list whole), the API server drops
fields the CRD does not know, and `promtool` reads only the group fields
`name`, `interval`, `query_offset`, `limit`, `labels` and `rules`. These come
from the public source of Kubernetes v1.37.1, Prometheus Operator v0.94.1 and
Prometheus v3.15.0; the comments in `internal/action/prometheusrule` name the
files. With `networkPolicy.restrictEgress` on, allow egress to the Kubernetes
API server.

##### `kubernetes.rollback`

Puts a Deployment, StatefulSet or DaemonSet of the cluster Sidekick runs in
back on an earlier revision, through the Kubernetes API, as `kubectl rollout
undo` does. It is approval-only: a route in `read-write` mode may list it only
when the action sets `allowReadWrite: true`, so use it on routes with
`mode: approved`. The approver sees both revisions, with their images, when
they were created and, for the running one, who deployed it as far as the
cluster knows. The action does not wait for the rollout to become healthy;
the watcher reports the recovery.

```yaml
actions:
  rollback-shop:
    type: kubernetes.rollback
    serviceAccount: sidekick-kubernetes-rollback   # default; the account the tokens are for
    allowReadWrite: false                          # default; see the note below
    maxRevisionsBack: 1                            # default 1, from 1 to 10
    limits:
      namespaces: [shop]                           # namespaces it may write in
      kinds: [Deployment, StatefulSet]             # default: all three kinds
      names: [checkout, ledger]                    # workload names, exact

routes:
  - name: shop
    mode: approved
    actions: [rollback-shop]
```

- `limits.namespaces` and `limits.names` are exact names, and each needs at
  least one. A workload is allowed when its namespace is in `namespaces`, its
  kind is in `kinds` and its name is in `names`. There are no patterns,
  because the Roles that the chart makes list the names as `resourceNames`.
- `limits.kinds` lists `Deployment`, `StatefulSet` and `DaemonSet`. Empty
  means all three.
- `maxRevisionsBack` is how many revisions back the model may ask to go. The
  model proposes `kind`, `namespace`, `name` and `revisionsBack` (default 1).
  A request for more than the limit is refused and the model reads why.
- `serviceAccount` is the name of a service account in Sidekick's own
  namespace. The action asks the API server for a token for it
  (`TokenRequest`) each time it proposes, runs or undoes a rollback, and the
  token lives 10 minutes. Sidekick's own account can only ask for tokens of
  the accounts that actions use, and this account can only `get` and `patch`
  the workloads of the limits. Sidekick must mount its service account
  token, which the chart does when it finds such an action. The default
  differs from the account of `prometheus.rule`, so the two types do not
  share rights unless you point both at one account.
- The action works on the cluster Sidekick runs in only. Other clusters are
  not supported.
- `allowReadWrite: true` lets a `read-write` route list the action, which
  then rolls back without a person. Leave it off unless you understand that
  a rollback goes live at once.

**What the chart creates.** For each `kubernetes.rollback` action in `config`
(turn this off with `rbac.actions: false` and make these yourself), the chart
makes the service account, adds it to the Role in Sidekick's namespace that
lets Sidekick's own account create tokens, and in each listed namespace makes
a Role and a RoleBinding. For the config above, the Role in `shop` is:

```yaml
rules:
  - apiGroups: [apps]
    resources: [deployments]
    resourceNames: ["checkout", "ledger"]
    verbs: [get, patch]
  - apiGroups: [apps]
    resources: [statefulsets]
    resourceNames: ["checkout", "ledger"]
    verbs: [get, patch]
  - apiGroups: [apps]
    resources: [replicasets]       # only with Deployment in kinds
    verbs: [list]
  - apiGroups: [apps]
    resources: [controllerrevisions]   # only with StatefulSet or DaemonSet in kinds
    verbs: [list]
```

The `get` and `patch` rules name the workloads. The `list` rules cannot: the
API server limits a `list` by name only when the request has a
`metadata.name` field selector, and the action lists revisions by label.
So the account can read the ReplicaSets or ControllerRevisions of the whole
namespace, and nothing else. It has no `update`, `delete` or `create`, and no
access to pods, Secrets or ConfigMaps.

**What it refuses.** When the model proposes, Sidekick reads the workload
and its revisions and refuses, with a message the model can act on:

- a workload with no earlier revision, or fewer than `revisionsBack`:
  Kubernetes keeps old ReplicaSets and ControllerRevisions only up to
  `revisionHistoryLimit`, so with `0`, or after one deploy, there is nothing
  to go back to;
- a paused Deployment, because the rollback would not roll out until someone
  resumes it;
- a workload managed by Argo CD or Flux, because their controllers would undo
  the change. It is recognised by the annotation
  `argocd.argoproj.io/tracking-id` or the label `argocd.argoproj.io/instance`
  (Argo CD), or the labels `kustomize.toolkit.fluxcd.io/name` or
  `helm.toolkit.fluxcd.io/name` (Flux). The message tells the model to
  propose `argocd.rollback`, if it is offered, or a pull request that reverts
  the change in Git. Argo CD 3.x tracks by that annotation by default, so the
  default case is detected. An installation that switched to label tracking
  with `app.kubernetes.io/instance` is not, because other tools, Helm among
  them, use that label too;
- a workload that has a change its controller has not processed yet, or whose
  current pod template no revision has yet. Read it again in a minute.

A workload that has only the labels of Helm itself is allowed. Helm will not
know about the rollback, and its next `helm upgrade` writes its own values
again; use `helm.rollback`, if it is offered, when Helm owns the release.

**Which revision.** The running revision is the one whose pod template the
workload has now. "N back" is the Nth earlier revision with a different pod
template, newest first, so a revision that Kubernetes recorded twice with the
same template counts once. A Deployment's revisions are its ReplicaSets, in
the order of the annotation `deployment.kubernetes.io/revision`; a
StatefulSet's and a DaemonSet's are their ControllerRevisions, in the order of
`revision`. The rollback replaces `spec.template` only, as `kubectl` does. It
does not touch replicas, the strategy or the annotations, so the
`kubernetes.io/change-cause` of the Deployment is not restored to the older
one. A StatefulSet or DaemonSet with `updateStrategy` `OnDelete`, or a
`partition`, does not replace its pods by itself after a rollback.

**What the approver sees.** For each of the two revisions: its kind (a
ReplicaSet or a ControllerRevision), its name and number, the image of every
container (`name=image` when there are several), when it was created, and its
notes (init containers, and the change cause when one was recorded). The
running revision also shows who last changed the pod template, from the
managed fields of the workload: the manager and the time. The cluster keeps no
such record for an earlier revision, so it shows as unknown, and so does the
running one when the managed fields say nothing.

**Conflicts.** Before it writes, the action reads the workload again. If it
has the pod template of the earlier revision already, which happens after a
crash and a retry, it writes nothing and succeeds. If its pod template is no
longer the one the approver read as running (someone deployed or edited it
since), or the earlier revision is gone or has other images, the proposal
fails as a conflict and a new one is needed. The patch holds the
`resourceVersion` of that read, so the API server answers 409 if the workload
changes between the read and the write, which also fails as a conflict.
Scaling the workload is not a conflict.

**Undo.** Undo puts back the revision that ran before the rollback. It finds
the revisions by name and pod template, never by number alone, because
Kubernetes gives a revision a new number when a rollback goes back to it. If
the workload does not run the revision the rollback wrote, Undo shows what it
runs now and asks before it restores anyway. If the revision to restore has
been deleted from the history, it cannot be restored.

**What it relies on.** A rollback is a strategic merge patch that replaces
`spec.template`. For a Deployment the template is the one of the ReplicaSet
without the `pod-template-hash` label, and for a StatefulSet or DaemonSet it
is the stored data of the ControllerRevision, as `kubectl` does. The patch
carries `metadata.resourceVersion`, and the API server refuses it with 409
when that is not the stored one. After the patch, the controllers give the
older revision the next number. These come from the public source of
Kubernetes v1.37.1, Argo CD v3.5.7, Flux kustomize-controller v1.10.1 and
helm-controller v1.7.1; the comments in `internal/action/kubernetesrollback`
name the files. With `networkPolicy.restrictEgress` on, allow egress to the
Kubernetes API server.

##### `alertmanager.silence`

Silences alerts in a Prometheus Alertmanager for a limited time, for planned
work or a known noisy period. A silence mutes alerts; it does not close
them, and Alertmanager keeps the silence after it ends. Use it on routes
with `mode: approved`: the silence starts as soon as it is approved. It
needs no `allowReadWrite`.

```yaml
actions:
  mute-checkout:
    type: alertmanager.silence
    url: https://alertmanager.example.com    # the part before /api/v2
    bearerTokenSecret: { name: sidekick-alertmanager-silence, key: token }
    createdBy: sidekick                      # the name Alertmanager shows; default sidekick
    limits:
      maxDuration: 2h                        # default 2h, at most 24h
      scopes:                                # at least one
        - labels:
            alertname: [HighLatency, HighErrorRate]
            service: ["checkout-*"]
        - labels:
            team: [payments]

routes:
  - name: checkout-alerts
    match: { service: "checkout-*" }
    sources: [prometheus]
    mode: approved
    actions: [mute-checkout]
```

- `url` is the root of the Alertmanager API, the part before `/api/v2`. It
  may hold a path: for Grafana Mimir it is the address with
  `/alertmanager`, such as `https://mimir.example.com/alertmanager`. It must
  be https when the action sends a credential, except for a loopback host.
- `bearerTokenSecret`, or `basicAuth` (`username` and `passwordSecret`), or
  neither; `headers`, such as `X-Scope-OrgID` for the tenant of a Mimir
  (not `Authorization`, `Proxy-Authorization` or `Cookie`); and `tls`
  (`caSecret`, and `certSecret` with `keySecret`) take the same form as for
  the `alertmanagerURL` of a [Prometheus source](#prometheus-sources-type-prometheus).
  They are the action's own: give it a credential apart from the read-only
  one of the source. Secrets are read on every call, so a rotated one needs
  no restart.
- `createdBy` is the name Alertmanager shows as the creator. It is at most
  100 bytes.
- `limits.scopes` says which alerts may be silenced, and is required. A
  scope is a set of labels, each with the values it allows, as globs where
  `*` matches any text and every other character stands for itself. A
  scope with no labels is refused, because it would allow silencing
  everything.
- `limits.maxDuration` is the longest silence: from `5m` to `24h`, `2h`
  when not set. The shortest silence is 5 minutes.

**The rule for a silence.** The model proposes `matchers` (each with
`name`, `value`, `isRegex` and `isEqual`, as the Alertmanager API writes
them), a `duration` such as `30m` or `2h`, and a `comment` that says why.
A silence is allowed when, for at least one scope, every label of the scope
is pinned in the silence by an equality matcher (`isEqual: true`,
`isRegex: false`) whose value matches one of that label's globs. With the
scopes above, `alertname="HighLatency"` with `service="checkout-api"` is
allowed, and so is `team="payments"` alone. A regex or a not-equal matcher
on a scope label does not count as pinning it. Any further matcher is
allowed, whatever its kind, because it can only narrow what is silenced. A
silence has at most 10 matchers, and no matcher may have an empty name or an
empty value. The label names must be letters, digits and underscores. The
comment is at most 500 characters, and line breaks and tabs become spaces;
text with other control characters is refused.

**What the approver sees.** The proposal shows the silence as it will be
made: the matchers, the duration, the creator and the comment. It also shows
the firing alerts that the matchers match at the time of the proposal, as a
count and the first five, each with its state. That is information and not a
limit: a silence made ahead of planned work may match no alert yet. If
Alertmanager cannot be asked, the proposal is shown without the alerts and
says why. The silence starts when it is approved, not when it was proposed,
and lasts the duration the approver read.

**The marker.** Alertmanager has no way to recognise a repeated request, so
Sidekick adds a line to the comment, `[sidekick proposal <id>]`. It shows
people who made the silence, and a retry after a crash and Undo find the
silence by it. Before it makes a silence, Sidekick looks for one with the
marker in any state, so a crash makes one silence, and a silence that a
person ended in the meantime is not made again. Do not remove the line when
you edit a silence in Alertmanager: Undo could not find it then.

**Undo.** An approver can end a silence early. Undo ends the silence that
has the marker. A silence that has ended already counts as success. If
someone edited the silence since, so that its matchers or its length are not
what was written, Undo ends nothing and shows the silence as it is now; the
approver then confirms to end it anyway. Undo does not check the scopes
again, because ending a silence is the safe direction.

**Permissions.** Alertmanager has no permission model of its own: anyone who
can reach its API can make, change and end any silence. The credential of
this action should therefore reach only the calls the action makes, which a
reverse proxy in front of Alertmanager can enforce:

- `POST /api/v2/silences`, to make a silence
- `GET /api/v2/silences`, to find it by the marker
- `DELETE /api/v2/silence/{id}`, to end it
- `GET /api/v2/alerts`, for the proposal's preview

For example, with nginx, a server block that is used only by Sidekick's
silence credential, apart from the read-only access the Prometheus source
uses:

```nginx
map $http_authorization $sidekick_silence {
    default                        0;
    "Bearer REPLACE_WITH_THE_TOKEN" 1;
}

server {
    listen 8443 ssl;
    # ssl_certificate and ssl_certificate_key as for any other server

    if ($sidekick_silence = 0) { return 401; }

    location = /api/v2/silences {
        limit_except GET POST { deny all; }
        proxy_pass http://alertmanager:9093;
    }

    location = /api/v2/alerts {
        limit_except GET { deny all; }
        proxy_pass http://alertmanager:9093;
    }

    location ~ "^/api/v2/silence/[0-9a-f-]{36}$" {
        limit_except DELETE { deny all; }
        proxy_pass http://alertmanager:9093;
    }

    location / { return 403; }
}
```

Point `url` at this server. The proxy narrows which calls the credential can
make; it cannot read the body of a request. The scopes and the duration
limit are enforced by Sidekick, so a person who holds the token can still
make any silence directly, and a post that names the ID of an existing
silence changes that silence. Keep the token in a Secret that only
Sidekick reads. The example was written for this page and was not run
against a real server; check it with your own nginx before you rely on it.
For a Mimir, use the same idea in the gateway that fronts it, and set the
tenant in `headers`. The [Prometheus source](#prometheus-sources-type-prometheus)
keeps its own read-only credential.

**What it relies on.** The calls and fields come from the public source of
Alertmanager v0.34.1 and of Grafana Mimir mimir-3.1.8; the comments in
`internal/action/alertmanagersilence` name the files. A silence that is
edited in the Alertmanager UI keeps its ID when only its end changes and
gets a new ID when its matchers change, and the comment, with the marker, is
copied either way.

##### `grafana.silence`

Silences alerts in Grafana for a limited time, for planned work or a known
noisy period. It uses the Alertmanager that is built into Grafana alerting,
through Grafana's HTTP API. A silence mutes alerts; it does not close them,
and Grafana keeps the silence after it ends. It works like
[`alertmanager.silence`](#alertmanagersilence), with the same scopes and the
same duration limit. Use it on routes with `mode: approved`: the silence
starts as soon as it is approved. It needs no `allowReadWrite`. It supports
Grafana 11.6 and later. For silences in an Alertmanager outside Grafana, use
`alertmanager.silence`.

```yaml
actions:
  mute-payments:
    type: grafana.silence
    url: https://acme.grafana.net            # the root of Grafana; https, or http on localhost
    tokenSecret: { name: sidekick-grafana-silence, key: token }
    createdBy: sidekick                      # the name Grafana shows; default sidekick
    limits:
      maxDuration: 2h                        # default 2h, at most 24h
      scopes:                                # at least one
        - labels:
            alertname: [HighLatency, HighErrorRate]
            grafana_folder: ["payments-*"]
        - labels:
            team: [payments]

routes:
  - name: payments-alerts
    match: { grafana_folder: "payments-*" }
    sources: [prometheus]
    mode: approved
    actions: [mute-payments]
```

- `url` is the root of Grafana, such as `https://acme.grafana.net`. If Grafana
  is served from a sub-path, include it. It must be https, except for a
  loopback host.
- `tokenSecret` is a Grafana service account token. Use a token of its own,
  apart from the token of any Grafana source and from the one of
  `grafana.alert_rule`. It is read on every call, so a rotated token needs no
  restart. The permissions it needs are below.
- `tls` (`caSecret`, and `certSecret` with `keySecret`) takes the same form as
  for the [Prometheus source](#prometheus-sources-type-prometheus). Use it for a
  Grafana that is served with a certificate from your own CA.
- `createdBy` is the name Grafana shows as the creator. It is at most 100
  bytes. Grafana stores it as sent and does not replace it with the name of
  the service account.
- `limits.scopes` says which alerts may be silenced, and is required. A scope
  is a set of labels, each with the values it allows, as globs where `*`
  matches any text and every other character stands for itself. A scope with
  no labels is refused, because it would allow silencing everything. The labels
  are the labels of the alerts in Grafana, such as `alertname`,
  `grafana_folder` and the labels of the rule.
- `limits.maxDuration` is the longest silence: from `5m` to `24h`, `2h` when
  not set. The shortest silence is 5 minutes.

There is no key for the organization. A service account token belongs to one
Grafana organization, and the action works in that organization. Do not send
an `X-Grafana-Org-Id` header for another one: Grafana refuses it with a 401,
so the action has no `headers` key.

**The rule for a silence.** The model proposes `matchers` (each with `name`,
`value`, `isRegex` and `isEqual`), a `duration` such as `30m` or `2h`, and a
`comment` that says why. A silence is allowed when, for at least one scope,
every label of the scope is pinned in the silence by an equality matcher
(`isEqual: true`, `isRegex: false`) whose value matches one of that label's
globs. With the scopes above, `alertname="HighLatency"` with
`grafana_folder="payments-eu"` is allowed, and so is `team="payments"` alone.
A regex or a not-equal matcher on a scope label does not count as pinning it.
Any further matcher is allowed, because it can only narrow what is silenced.
A silence has at most 10 matchers, and no matcher may have an empty name or an
empty value. The comment is at most 500 characters, and line breaks and tabs
become spaces. The limits are checked when the model proposes and again
right before the silence is made.

**What the approver sees.** The proposal shows the silence as it will be made:
the matchers, the duration, the creator and the comment. It also shows the
firing alerts that the matchers match at the time of the proposal, as a count
and the first five, each with its state. That is information and not a limit.
If Grafana cannot be asked, for example because the token may not read alerts,
the proposal is shown without the alerts and says why. The silence starts
when it is approved, not when it was proposed, and lasts the duration the
approver read.

**The marker.** Sidekick adds a line to the comment, `[sidekick proposal <id>]`.
It shows people who made the silence, and a retry after a crash and Undo find
the silence by it. Before it makes a silence, Sidekick looks for one with the
marker in any state, so a crash makes one silence, and a silence that a person
ended in the meantime is not made again. Do not remove the line when you edit
a silence in Grafana: Undo could not find it then.

**Edits in Grafana.** When a person edits a silence in Grafana, it keeps its
ID if the matchers and the start are the same, as when only the end changes.
If the matchers change, Grafana ends the old silence and makes a new one with a
new ID. The comment, with the marker, is copied either way, so Sidekick finds
the silence by the marker and not by its ID.

**Undo.** An approver can end a silence early. Undo ends the silence that has
the marker. A silence that has ended already counts as success. If someone
edited the silence since, so that its matchers or its length are not what was
written, Undo ends nothing and shows the silence as it is now; the approver
then confirms to end it anyway. Undo does not check the scopes again, because
ending a silence is the safe direction.

**Too many silences.** Grafana can limit how many silences it keeps. When it is
at its limit it answers a new silence with a 429, and the proposal fails with
the message "Grafana has reached its limit on silences; remove old silences in
Grafana (Alerting, Silences) and try again". Sidekick tries again as it does for
any 429, which works if someone removes old silences in time.

**Permissions.** Create a service account for Sidekick and give it only these
three permissions, without a scope:

- `alert.instances:read`, to list silences, to find the marker and for the
  preview of firing alerts
- `alert.instances:create`, to make a silence
- `alert.instances:write`, to end a silence

On Grafana Enterprise and Grafana Cloud, put them in a custom role. On open
source Grafana there are no custom roles, and the Editor role holds them, so
the service account needs the Editor role, which can also edit other things in
the organization; keep the token as secret as any other credential that can do
that.

These permissions work in the whole organization. Grafana cannot limit them to
alerts with certain labels, so the scopes and the duration limit that Sidekick
enforces are the only limit on what is silenced, and a person who holds the
token can make any silence in the organization directly. Keep it in a Secret
that only Sidekick reads.

The `alert.silences:read`, `alert.silences:create` and `alert.silences:write`
permissions do not replace them. Grafana checks those only for a silence that
names one alert rule with a `__alert_rule_uid__` matcher, and limits them to
the folder of that rule. A silence on other labels, which is what the scopes
describe, needs `alert.instances:create` to be made and `alert.instances:write`
to be ended.

**What it relies on.** The calls, fields and permissions come from the public
source and the published API description of
[grafana/grafana](https://github.com/grafana/grafana), checked at v13.2.5,
v12.4.14 and v11.6.9; the silence calls and their permission checks are the
same in v13.3.0. The comments in `internal/action/grafanasilence` name the
files. Grafana answers 202 and not 200 when it makes a silence. In a Grafana
that runs on several servers, silences are shared between them within a short
time, so a retry that reaches another server right after a crash could not see
the silence yet and make a second one.

##### `launchdarkly.flag`

Turns a LaunchDarkly feature flag off in one environment through the
LaunchDarkly API, and can turn it back on. Only turning a flag off is
supported: putting the earlier targeting back would need the full previous
configuration of the environment, and LaunchDarkly's public documentation of
its audit log does not show that an entry holds it. The action is
approval-only (see [action modes](#action-modes)): a `read-write` route may
list it only when the action sets `allowReadWrite: true`. Use it on routes
with `mode: approved`.

```yaml
actions:
  checkout-flag-off:
    type: launchdarkly.flag
    baseURL: https://app.launchdarkly.com   # default; https://app.eu.launchdarkly.com for EU
    tokenSecret: { name: sidekick-launchdarkly-write, key: token }
    allowReadWrite: false                   # default
    limits:
      projects:
        - key: shop
          environments: [production]
          flags: ["checkout-*", new-search] # keys, or patterns where * is any text

routes:
  - name: checkout-errors
    match: { intake: [alertmanager] }
    mode: approved
    actions: [checkout-flag-off]
```

| Key | Meaning |
|---|---|
| `tokenSecret` | A LaunchDarkly access token. Required. Use a token of its own with the custom role below, apart from the token of any LaunchDarkly source. It is read on every call, so a rotated token needs no restart. |
| `baseURL` | The root of the instance: `https://app.eu.launchdarkly.com` for EU, `https://app.launchdarkly.us` for Federal. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses. Default: `https://app.launchdarkly.com`. |
| `allowReadWrite` | See [action modes](#action-modes). Default: false. |
| `limits.projects` | Required. Each entry has a project `key`, its `environments` and its `flags`, and all three are required, so nothing is allowed by default. A flag entry is a key, or a pattern in which `*` stands for any text and is the only wildcard. |

The model proposes `project`, `environment`, `flag` and `change`, which is
`turn_off`. A project, environment or flag outside the limits is refused
before anything is sent. No other environment of the flag changes.

**What it checks.** When the model proposes, Sidekick reads the flag and
refuses a flag that is not found, archived or already off. It also refuses
what LaunchDarkly would refuse to change directly, so the approver is not
shown something that will fail: an environment that requires approval
for the flag (`approvalSettings`, which apply to every flag or only to flags
with one of `requiredApprovalTags`), a scheduled change for the flag in that
environment, and an active workflow. The message says what to do, and the
model reads it. If one of these three reads fails, for example because the
token is not allowed to make it, the proposal is not refused for that: a
warning is logged, and LaunchDarkly's own answer when the change is written
(405 when approvals are required, 400 when the change would make a
scheduled change or an approval request fail) ends the proposal as failed,
with a plain message. Sidekick never sends `ignoreConflicts`. Approvers see
one field, `on`, change from `true` to `false`.

**Conflicts.** Right before it writes, Sidekick reads the flag again. If
the environment's `version` is not the one at the proposal, or the flag was
archived since, the proposal fails as a conflict and a new one is needed. A
flag that is already off, which is what a repeat after a crash finds, is
left alone and counts as done. LaunchDarkly has no precondition on the
version for a semantic patch, so a change made in the short time between this
read and the write is not detected, as for `datadog.monitor`. The change is
made with the `turnFlagOff` instruction of a semantic patch, with a comment
that names the proposal, so the flag's audit log says who turned it off.

**Undo.** Undo turns the flag back on in that environment with `turnFlagOn`,
and does nothing when the flag is on already. The action changes one setting,
which is either on or off, so there is no later edit of that setting for
`force` to confirm; edits to other settings of the flag stay.

**The custom role.** Create a custom role for the token that can do only this,
with `basePermissions` set to `no_access`, so it grants only what the policy
allows. For the limits above:

```json
{
  "key": "sidekick-flag-off",
  "name": "Sidekick flag off",
  "basePermissions": "no_access",
  "policy": [
    { "effect": "allow", "actions": ["viewProject"], "resources": ["proj/shop"] },
    { "effect": "allow", "actions": ["updateOn"], "resources": ["proj/shop:env/production:flag/checkout-*"] }
  ]
}
```

`updateOn` turns targeting on or off, for the current environment only, and
`viewProject` lets the token see the project and its flags. Add one pair for
each project, environment and flag of `limits.projects`. LaunchDarkly's public
documentation does not say which action the reads of the environment's
approval settings, the scheduled changes and the workflows need. Without the
right to make them, Sidekick cannot check these before the proposal and relies
on LaunchDarkly's refusal when the change is written, as described above. The
role gives the token no right to change targeting rules, targets, variations,
approvals or workflows, to archive or delete flags, or to touch another
environment.

**What it relies on.** The facts come from LaunchDarkly's public OpenAPI file
and documentation, as the comments in `internal/action/launchdarklyflag` say.
Nothing was tried against a LaunchDarkly account.

##### `gcp.cloud_run_rollback`

Sends the traffic of a Cloud Run service back to an earlier ready revision,
through the Cloud Run Admin API (v2). The approver sees the revision that
serves now and the one it goes back to, and can undo it. The action is
approval-only (see [action modes](#action-modes)): a `read-write` route may
list it only when the action sets `allowReadWrite: true`. Use it on routes
with `mode: approved`. It changes only the traffic of the service: it never
deploys, splits traffic or waits for the service to be healthy. The watcher
reports when the service recovers.

**A rollback pins the service.** After a rollback, all the traffic goes to
one revision by name. The service no longer follows the latest revision, so
later deploys get no traffic until traffic is set back. The proposal says
this, and so does the result. Undo puts the traffic list back as it was,
for example 100 percent to the latest revision, so deploys flow again. If you
do not undo the rollback, send the traffic back yourself when the fix is
deployed, for example with `gcloud run services update-traffic <service>
--to-latest`.

```yaml
actions:
  checkout-rollback:
    type: gcp.cloud_run_rollback
    credentialsSecret: { name: sidekick-gcp-run, key: credentials.json }
    allowReadWrite: false                 # default
    maxRevisionsBack: 2                   # default 1; from 1 to 10
    limits:
      projects: [shop-prod]
      locations: [europe-west1]
      services: [checkout, "payments-*"]  # names, or patterns where * is any text

routes:
  - name: checkout-errors
    match: { intake: [alertmanager] }
    mode: approved
    actions: [checkout-rollback]
```

| Key | Meaning |
|---|---|
| `credentialsSecret` | A credential file in a Secret: a workload identity federation file, or a service account key. Empty means Application Default Credentials, which on GKE come from Workload Identity. Read again for each new token, so a rotated file needs no restart. Use a credential of its own with the custom role below, apart from the one of any [Google Cloud source](sources/gcp.md). With it empty, the action and the source use the same Google identity of the pod, which then needs the rights of both. |
| `allowReadWrite` | See [action modes](#action-modes). Default: false. |
| `maxRevisionsBack` | How many revisions back the model may ask to go. Default 1; a value you write must be from 1 to 10. |
| `limits.projects` | Required. The project IDs whose services may be rolled back. |
| `limits.locations` | Required. The regions of those services, such as `europe-west1`. |
| `limits.services` | Required. Service names, or patterns in which `*` stands for any text and is the only wildcard. |

The model proposes `service` and, when the config lists more than one, `project`
and `location`. It may add `revisionsBack`, which is 1 when left out. A value
outside the limits is refused before anything is sent.

**Which revision.** Sidekick reads the service and its revisions, which Google
Cloud lists newest first. The revision that serves now is the latest ready
revision when the traffic follows the latest revision (or has no list), and the
named revision when the traffic is pinned. "N back" is the Nth ready revision
older than that one: a revision that is not ready is skipped and not counted.
The proposal is refused, and the model reads why, when:

- the traffic is split between several revisions, or the traffic list has any
  entry besides one with 100 percent and no tag, because setting the traffic
  would drop it;
- the service is still applying an earlier change;
- the service has no ready revision, or too few earlier ready revisions;
- the list has more than 1000 revisions.

**What it checks.** Right before it writes, Sidekick reads the service again. If
its etag (the version tag Google Cloud gives each version of a service), the
way its traffic is set, or the revision that serves differ from the proposal,
a deploy or an edit happened since, and the proposal fails as a conflict. A
new proposal is needed. If all the traffic goes to the revision of the
proposal already, which is what a repeat after a crash finds, nothing is
written and it counts as done. The update sends the service as it was read,
with only `traffic` replaced and its live etag, and `updateMask=traffic`, and
waits for the operation within the 5 minute run. Google Cloud's public API
definitions say the etag "may be used to detect modification conflict", but
not that an update refuses a stale one, so Sidekick compares the etag itself
and also reads an answer of 409 as a conflict. A short moment between its own
read and the write is not covered.

**Undo.** Undo puts the traffic back: 100 percent to the latest revision when the
traffic followed it before, otherwise 100 percent to the revision that was
named. If the traffic is not what the rollback wrote, someone changed it, and
Undo changes nothing and shows the revision that serves now, unless the
approver confirms to restore it anyway (`force`). Undo does nothing when the
traffic is back already.

**The custom role.** Give the credential a custom role with only what the
action calls:

```sh
gcloud iam roles create sidekickCloudRunRollback --project <project> \
  --title "Sidekick Cloud Run rollback" \
  --permissions run.services.get,run.services.update,run.revisions.list,run.operations.get
```

| Permission | Used for |
|---|---|
| `run.services.get` | Reading the service and its traffic. |
| `run.services.update` | Changing the traffic. |
| `run.revisions.list` | Listing the revisions of the service. |
| `run.operations.get` | Waiting for the update to finish. |

Grant it on each project, or on each service, that `limits` lists. The role
gives no right to deploy, to delete revisions or services, or to change who may
call a service. Google's IAM reference lists these permissions in the Cloud
Run Developer and Viewer roles, and `run.services.update` only in Developer.
Google's pages do not say whether an update of only the traffic also needs
`iam.serviceAccounts.actAs` on the service's runtime service account, which
deploying a revision does need. Start without it. If an update answers 403 and
names that permission, grant Service Account User on that one service account
to the action's credential and nothing wider.

With `networkPolicy.restrictEgress`, allow `run.googleapis.com` and Google's
token endpoint on port 443 (`*.googleapis.com` covers both), and, on GKE with
Application Default Credentials, the metadata server at `169.254.169.254/32`.

**What it relies on.** The facts come from Google's public API definitions
(`googleapis/googleapis`, `google/cloud/run/v2`, and the discovery document
in `googleapis/google-api-go-client` v0.297.0), as the comments in
`internal/action/gcprun` say. Nothing was tried against a Google Cloud
project.

##### `github.pull_request`

Opens a pull request that changes monitor definitions kept in Git:
thresholds, durations, filters, exclusions, or the deletion of a monitor.
The model reads the current files with the [GitHub source](sources/github.md)
and proposes the whole new content of each file it changes. Use it on a
route in `approved` mode: a person reads the files before anything is
pushed.

```yaml
actions:
  monitor-pr:
    type: github.pull_request
    app:                              # a GitHub App of its own, not the source's
      appID: 123456
      installationID: 7890123
      privateKeySecret: { name: sidekick-github-actions, key: private-key.pem }
    # baseURL: https://github.example.com/api/v3/   # GitHub Enterprise Server
    limits:
      repos: [acme/monitors]          # owner/name, no wildcards
      paths:                          # the first glob that matches decides the format
        - { glob: "prometheus/rules/**", format: prometheus_rules }
        - { glob: "datadog/**",          format: terraform }
        - { glob: "grafana/alerting/**", format: grafana_alert_file }
      maxFiles: 3                     # 1 to 25
      maxChangedLines: 40             # lines added plus lines removed, 1 to 1000
      baseBranch: main                # the only branch a pull request may target
      draft: true                     # default true
```

- **Credentials.** Give the app its own installation on the monitors
  repositories only, with these repository permissions: Contents (read and
  write), Pull requests (read and write) and Metadata (read). Sidekick asks
  GitHub for installation tokens with just those permissions, and reads the
  private key from its secret on each token exchange, so a rotated key needs
  no restart. `baseURL` must be https, except for a loopback host.
- **Paths and formats.** Every file the model changes must match a `glob`,
  and the glob names its format. A path that matches none is refused. In a
  glob, `*` and `?` do not match a slash, and a segment of `**` matches any
  number of segments. The formats are:
  - `prometheus_rules`: a Prometheus rules file, checked with `promtool`;
  - `terraform`: a file of a Terraform or OpenTofu module (Datadog, SigNoz
    and Grafana providers), checked with `tofu validate` over the whole
    module directory;
  - `grafana_alert_file`: a Grafana alert rule provisioning file.
- **Validation.** Before the pull request is made, the files go to the
  [validator](validator.md), set up with the [`validator`](#validator)
  section and the chart. It runs when the model proposes, and again right
  before the pull request opens, against the base branch as it is then. For
  `terraform`, Sidekick reads the module directory of each changed file at
  the base branch, and the local modules it uses (`source = "../modules/x"`,
  up to 3 levels), applies the changes and validates the directory. If a
  file fails, the errors go back to the model as an error and nothing is
  stored; if they are found when the proposal runs, the proposal fails with
  them. No pull request opens in either case. If the validator cannot be
  reached, the proposal is refused when the model proposes, and the action
  is tried again when it runs.
- **What it does.** On approval it makes the branch `sidekick/<proposal id>`
  from the head of the base branch with one commit, and opens the pull
  request, as a draft unless `draft: false`. The description is what the
  model wrote, with `@mentions` and issue references after words such as
  "fixes" put in code spans so the pull request notifies nobody and closes
  nothing, and a line that names the proposal. If a worker dies half way, the
  retry finds the branch, and the pull request if it was made, and does not
  make a second one.
- **Limits.** `maxChangedLines` counts the lines that differ from the base
  branch in all files together. A file the model sends unchanged is refused,
  and so is a file larger than 256 KiB at the base. A request to change
  something outside the limits is refused when the model proposes, and again
  when the proposal runs. `maxFiles` is at most 25 because an approver reads
  up to 4 fields for each file (its blob, its status and its two line
  counts), and a change may differ in at most 100 fields in all.
- **What the approver reads.** The proposal shows each file with what happens
  to it (`changed`, `added` or `deleted`) and how many lines it adds and
  removes, next to the parameters, which hold the whole new content. Sidekick
  stores the blob SHAs of the files before and after, and the SHA of the base
  commit, never the file contents. A unified diff of the content is not shown.
- **Files that changed meanwhile.** The model sends the whole new content of
  each file. When the proposal runs, the action compares each file on the
  base branch with the blob it had when the model proposed. A file that
  someone changed meanwhile fails the proposal with a conflict that names the
  file, so the pull request never overwrites their edit; propose again from
  the new content. A file that already has the new content is left out, and
  if every file does, the proposal succeeds without a pull request.
- **Undo.** An approver can undo a succeeded proposal. If its pull request is
  still open or was closed, Sidekick closes it and deletes its branch. If it
  was merged, Sidekick opens a revert pull request from the branch
  `sidekick/<undo proposal id>`, as a draft unless `draft: false`, that
  restores the earlier content of the files (by blob, with no format
  validation, because the files only go back to what they were). The revert
  pull request still needs review and merging; the undo is done once it is
  opened. If a file on the base branch is not as the merged pull request left
  it, nothing is restored until the approver confirms, and then the earlier
  content overwrites it. The revert pull request is tracked like any other.

Once a day, Sidekick reads from GitHub whether the pull requests it opened
are still open, were merged or were closed, and records it in
`pull_request_states`. Every replica checks once an hour, and at start-up,
that the job of the day (in UTC) is queued; however many replicas there
are, it runs once a day. It looks at the pull requests of proposals that
succeeded in the last 90 days, until each is merged or closed, and reads at
most 1,000 a day. The merged ones feed the count of noisy alerts fixed. A
pull request that stays open for more than 90 days is no longer checked.
A row is deleted with its proposal, so these records last as long as the
[`retention.investigations`](#retention) period of their investigation.

Sidekick counts the pull requests it opens in
`sidekick_githubpr_pull_requests_total` (by `result`: `opened`, `existing`,
`applied` for a change already on the base branch, `closed` or `reverted` for
an undo, or `error`), the validations in `sidekick_githubpr_validations_total`
(by `format` and `result`), and the daily checks in
`sidekick_githubpr_status_checks_total` (by `state`: `open`, `merged`,
`closed`, `missing` or `error`).

##### `github.code_change`

Opens a draft pull request that changes application code, in a repository you
allow. Use it when an investigation finds a bug in code that a small change
fixes. The model reads the current files with the [GitHub
source](sources/github.md) and proposes the whole new content of each file it
changes. It is not for monitors: [`github.pull_request`](#githubpull_request)
changes monitor definitions, with its own limits and its own app, so a team
can allow one without the other. Use it on a route in `approved` mode: a
person reads the change before anything is pushed.

The pull request is always a draft, and Sidekick never merges it, turns on
auto-merge, asks for a review or assigns anyone. The repository's own checks
run on it, and a person reviews and merges it. There is no `draft` key, so a
config that sets `draft` is an unknown key error. The action has no validator;
the repository's own checks are the validator. It is not approval-only, as
`github.pull_request` is not, but a draft pull request that nobody merges
changes nothing, and the examples use `mode: approved`.

```yaml
actions:
  code-change:
    type: github.code_change
    app:                              # a GitHub App of its own, not the source's
      appID: 123456
      installationID: 7890123
      privateKeySecret: { name: sidekick-github-code, key: private-key.pem }
    # baseURL: https://github.example.com/api/v3/   # GitHub Enterprise Server
    limits:
      repos: [acme/checkout]          # owner/name, no wildcards
      paths: ["src/**", "internal/**/*.go"]   # globs of the files that may change
      # refusedPaths: ["src/legacy/**"]       # added to the built-in refused paths
      maxFiles: 3                     # 1 to 25
      maxChangedLines: 60             # lines added plus lines removed, 1 to 1000
      baseBranch: main                # the only branch a pull request may target
      allowDelete: false              # default false

routes:
  - name: checkout
    mode: approved
    actions: [code-change]
```

- **Credentials.** Give the app its own installation on the repositories in
  `limits.repos` only, with these repository permissions: Contents (read and
  write), Pull requests (read and write) and Metadata (read). Do not give the
  app the Workflows permission. Sidekick asks GitHub for installation tokens
  with just the three permissions above, so its token never carries Workflows
  either way. Use an app that no other action or source shares. Sidekick reads
  the private key from its secret on each token exchange, so a rotated key
  needs no restart. `baseURL` must be https, except for a loopback host.
- **Paths.** Every file the model changes must match a glob of `paths`, and
  none of the refused paths. In a glob, `*` and `?` do not match a slash, and a
  segment of `**` matches any number of segments. A path that matches a
  refused path is refused even when a glob of `paths` allows it, for example
  with `paths: ["**"]`. Paths are compared as they are, except that refused
  paths ignore case. The path is checked when the model proposes and again
  when the proposal runs, so a change to the config in between applies.
- **Built-in refused paths.** These are always on. You can add to them with
  `refusedPaths`, but you cannot remove any:
  - CI and build automation: `.github/**`, `.gitlab/**`, `.circleci/**`,
    `.buildkite/**`, `.tekton/**`, `.gitea/**`, `.forgejo/**`,
    `.woodpecker/**`, `.semaphore/**`, `.azure-pipelines/**`, `.drone.yml`,
    `.travis.yml`, `.woodpecker.yml`, `.appveyor.yml`, `.cirrus.yml`,
    `appveyor.yml`, `bitbucket-pipelines.yml`, `buildspec.yml`,
    `**/.gitlab-ci.yml`, `**/Jenkinsfile*`, `**/azure-pipelines*.yml`,
    `**/cloudbuild.y*ml`;
  - ownership and review rules: `CODEOWNERS`, `**/CODEOWNERS`,
    `renovate.json*`, `.renovaterc*`;
  - Git settings and hooks: `.gitmodules`, `**/.gitattributes`, `.husky/**`,
    `.githooks/**`, `.pre-commit-config.yaml`, `lefthook.y*ml`;
  - secrets and keys: `**/.env`, `**/.env.*`, `**/*.env`, `**/.envrc`,
    `**/*.pem`, `**/*.key`, `**/*.p12`, `**/*.pfx`, `**/*.p8`, `**/*.jks`,
    `**/*.keystore`, `**/id_rsa*`, `**/id_ed25519*`, `**/id_ecdsa*`,
    `**/id_dsa*`, `**/.npmrc`, `**/.pypirc`, `**/.netrc`,
    `**/.git-credentials`, `**/.htpasswd`, `**/.aws/credentials`,
    `**/.docker/config.json`, `**/.kube/config`, `**/*.tfvars`,
    `**/*.tfvars.json`, `**/*.tfstate`, `**/*.tfstate.*`, `**/secrets/**`.
- **What the model may send.** The whole new content of each file, or a
  delete. A delete is refused unless `allowDelete` is true. A file must be at
  most 256 KiB, at the base branch and in the new content, and must be UTF-8
  text. Sidekick refuses a file that is binary, a symlink, a submodule or a
  directory, and a file the model sends unchanged. A file that exists keeps its
  mode (`100644` or `100755`), and a new file is written as `100644`.
- **What it checks.** When the model proposes, Sidekick reads the head of the
  base branch and each changed file at it, counts the lines that differ from the
  base branch in all files together, and applies every limit above. The
  errors go back to the model, which can propose again. The same checks run
  again when the proposal runs, against the base branch as it is then. A file
  is read through the Git trees API, which says what each entry is.
- **What it does.** On approval it makes the branch `sidekick/<proposal id>`
  from the head of the base branch with one commit, and opens the draft pull
  request. The description is what the model wrote, with `@mentions` and issue
  references after words such as "fixes" put in code spans so the pull
  request notifies nobody and closes nothing, and a line that names the
  proposal. If a worker dies half way, the retry finds the branch, and the
  pull request if it was made, and does not make a second one. If GitHub does
  not open the pull request as a draft, which can happen for a private
  repository on a plan without drafts, Sidekick closes it, deletes the branch
  and fails the proposal with that reason. A retry that finds a pull request
  of its own that is not a draft does the same, so it never leaves one that
  could be merged as it is, unless a person merged it. The proposal's result holds the
  number and the head SHA of the pull request.
- **What the approver reads.** The proposal shows each file with what happens
  to it (`changed`, `added` or `deleted`) and how many lines it adds and
  removes, and the line diff of each file in the web UI (see [action
  proposals](web-ui.md#action-proposals)). Sidekick stores the blob SHAs of
  the files before and after, and the SHA of the base commit, never the file
  contents.
- **Files that changed meanwhile.** When the proposal runs, the action
  compares each file on the base branch with the blob it had when the model
  proposed. A file that someone changed meanwhile fails the proposal with a
  conflict that names the file, so the pull request never overwrites their
  edit; propose again from the new content. A file that already has the new
  content is left out, and if every file does, the proposal succeeds without a
  pull request.
- **Undo.** An approver can undo a succeeded proposal. If its pull request is
  still open or was closed, Sidekick closes it and deletes its branch. If it
  was merged, Sidekick opens a draft revert pull request from the branch
  `sidekick/<undo proposal id>` that restores the earlier content of the
  files, by blob. A file the pull request added is deleted, and a file it
  deleted comes back with the mode it had. The revert pull request still needs
  review and merging; the undo is done once it is opened. If a file on the base
  branch is not as the merged pull request left it, nothing is restored until
  the approver confirms, and then the earlier content overwrites it. A file
  that is a symlink, a submodule or a directory by then is never overwritten,
  and a path that a refused path matches by then is refused.

Once a day, Sidekick reads from GitHub whether the pull requests it opened are
still open, were merged or were closed, as it does for `github.pull_request`
(see there). Sidekick counts the pull requests it opens in
`sidekick_githubcode_pull_requests_total` (by `result`: `opened`, `existing`,
`applied` for a change already on the base branch, `closed` or `reverted` for
an undo, or `error`), and the daily checks in
`sidekick_githubcode_status_checks_total` (by `state`: `open`, `merged`,
`closed`, `missing` or `error`). A pull request of this action does not count
as a noisy alert fixed.

##### `gitlab.merge_request`

Opens a merge request that changes monitor definitions kept in Git on
gitlab.com or a self-managed GitLab: thresholds, durations, filters,
exclusions, or the deletion of a monitor. It does for GitLab what
[`github.pull_request`](#githubpull_request) does for GitHub, with the same
limits, formats, validation, snapshot and Undo. The model reads the current
files with the [GitLab source](sources/gitlab.md) and proposes the whole new
content of each file it changes. Use it on a route in `approved` mode: a
person reads the files before anything is pushed.

```yaml
actions:
  monitor-mr:
    type: gitlab.merge_request
    tokenSecret: { name: sidekick-gitlab-actions, key: token }   # a token of its own, not the source's
    # baseURL: https://gitlab.example.com   # self-managed GitLab; default https://gitlab.com
    limits:
      projects: [acme/platform/monitors]    # full paths, no wildcards
      paths:                                # the first glob that matches decides the format
        - { glob: "prometheus/rules/**", format: prometheus_rules }
        - { glob: "datadog/**",          format: terraform }
        - { glob: "grafana/alerting/**", format: grafana_alert_file }
      maxFiles: 3                           # 1 to 25
      maxChangedLines: 40                   # lines added plus lines removed, 1 to 1000
      baseBranch: main                      # the only branch a merge request may target
      draft: true                           # default true
```

A self-managed GitLab with a project in a subgroup, and merge requests that
are ready for review:

```yaml
actions:
  monitor-mr:
    type: gitlab.merge_request
    baseURL: https://gitlab.example.com
    tokenSecret: { name: sidekick-gitlab-actions, key: token }
    limits:
      projects: [platform/observability/monitors]
      paths:
        - { glob: "rules/**", format: prometheus_rules }
      maxFiles: 5
      maxChangedLines: 100
      baseBranch: main
      draft: false

routes:
  - name: checkout
    mode: approved
    actions: [monitor-mr]
```

- **Credentials.** `tokenSecret` is the `{ name, key }` of the Secret that
  holds an access token with the `api` scope and the Developer role on the
  monitors projects only: a project access token is the simplest, and a group
  or personal access token works too. `api` is the only scope of a project or
  group token that allows the Commits and Merge Requests APIs, so the token
  can do everything the Developer role can do in that project. From GitLab
  19.2 a fine-grained personal access token can be limited to what the action
  calls, which is Branch (create, delete, read), Commit (create), Merge
  Request (create, read, update), and Repository (read, for files, the tree
  and blobs); it belongs to a user, so make a user for Sidekick. Give the
  action a token of its own, apart from the one of any GitLab source (that one
  needs only `read_api` and the Reporter role). The token is read on every
  call, so a rotated token needs no restart. `baseURL` is the root of your
  GitLab, without `/api/v4`; it must be https, except for a loopback host. A
  reverse proxy in front of GitLab must pass the encoded `%2F` in a path on as
  it is.
- **Protect the base branch.** Unlike a GitHub App, a token is not narrowed
  for each run, so the token's role and the protected base branch are the
  hard limit of what this action can do. Protect `baseBranch` (Settings,
  Repository, Branch rules), set **Allowed to push and merge** to **No one**
  and **Allowed to merge** to **Maintainers**. Then a Developer token cannot
  push to the branch or merge into it, whatever a merge request says. Set
  **Allowed to push and merge** explicitly: a value that is not set does not
  stop pushes. Branches named `sidekick/*` need no protection. A push rule of
  the project, such as one for commit messages or signed commits, applies to
  the commit Sidekick makes and may refuse it; the message of GitLab then
  goes into the error of the proposal.
- **Paths and formats.** Every file the model changes must match a `glob`,
  and the glob names its format. A path that matches none is refused. In a
  glob, `*` and `?` do not match a slash, and a segment of `**` matches any
  number of segments. The formats are `prometheus_rules`, `terraform` and
  `grafana_alert_file`, as for [`github.pull_request`](#githubpull_request).
- **Validation.** Before the branch is made, the files go to the
  [validator](validator.md), set up with the [`validator`](#validator)
  section and the chart. It runs when the model proposes, and again right
  before the merge request opens, against the base branch as it is then. For
  `terraform`, Sidekick reads the module directory of each changed file with
  the repository tree API at the head of the base branch, and the local
  modules it uses (`source = "../modules/x"`, up to 3 levels), applies the
  changes and validates the directory. If a file fails, the errors go back to
  the model as an error and nothing is stored; if they are found when the
  proposal runs, the proposal fails with them. No branch is made in either
  case. If the validator cannot be reached, the proposal is refused when the
  model proposes, and the action is tried again when it runs.
- **What it does.** On approval it makes the branch `sidekick/<proposal id>`
  and one commit with all files in one call of the Commits API, starting from
  the commit the files were read at, and opens the merge request from it. The
  title has the `Draft:` prefix unless `draft: false`, because GitLab marks a
  draft by its title; a draft prefix that the model wrote itself is removed
  first, so `draft: false` gives a merge request that is not a draft. Sidekick
  never sets an assignee, reviewers, labels, a milestone, auto-merge ("merge
  when the pipeline succeeds") or squash, and never approves or merges.
- **The description.** It is what the model wrote, cleaned, and a line that
  names the proposal. `@mentions`, `#12` and `!34` references (also with a
  project path, as `group/project#12`), and the target of a closing word such
  as "fixes" or "closes" (an issue, a URL or a Jira key) go in code spans, so
  the merge request notifies nobody, writes a note on nothing and closes
  nothing. GitLab runs a line that starts with a slash and a command name, such
  as `/merge`, `/approve`, `/assign`, `/label`, `/close`, `/draft` or `/ready`,
  as a quick action when it saves a description, with the rights of the
  token's user. So no line of the description, the title or the commit message
  starts with a slash: such a line is put in a code span too.
- **Retry after a crash.** The branch is named by the proposal, so a repeat
  looks for the merge request from that branch, in any state, and returns the
  one it finds without changing anything, even when its branch is gone after a
  merge. A branch with no merge request gets one; the commit exists with the
  branch, so it is not made twice. Two attempts at once end the same way.
- **Limits.** `maxChangedLines` counts the lines that differ from the base
  branch in all files together. A file the model sends unchanged is refused,
  and so is a file larger than 256 KiB at the base, a file that is not UTF-8
  text, and a path that is a directory. A request to change something outside
  the limits is refused when the model proposes, and again when the proposal
  runs. `maxFiles` is at most 25 because an approver reads up to 4 fields for
  each file (its blob, its status and its two line counts), and a change may
  differ in at most 100 fields in all. Repositories that use SHA-256 for their
  objects are not supported.
- **What the approver reads.** The proposal shows each file with what happens
  to it (`changed`, `added` or `deleted`) and how many lines it adds and
  removes, and the line diff of each file, read from GitLab by blob ID when a
  person opens it. Sidekick stores the blob IDs of the files before and after,
  and the SHA of the base commit, never the file contents.
- **Files that changed meanwhile.** When the proposal runs, the action
  compares each file on the base branch with the blob it had when the model
  proposed. A file that someone changed meanwhile fails the proposal with a
  conflict that names the file, so the merge request never overwrites their
  edit; propose again from the new content. A file that already has the new
  content is left out, and if every file does, the proposal succeeds without a
  merge request.
- **Undo.** An approver can undo a succeeded proposal. If its merge request is
  still open or was closed, Sidekick closes it and deletes its branch. If it
  was merged, Sidekick opens a revert merge request from the branch
  `sidekick/<undo proposal id>`, as a draft unless `draft: false`. It restores
  the earlier content of the files with the Commits API, not with GitLab's
  revert call, so GitLab and GitHub behave alike, and with no format
  validation, because the files only go back to what they were. The revert
  merge request still needs review and merging; the undo is done once it is
  opened. If a file on the base branch is not as the merged merge request left
  it, nothing is restored until the approver confirms, and then the earlier
  content overwrites it. The revert merge request is tracked like any other.

Once a day, Sidekick reads from GitLab whether the merge requests it opened
are still open, were merged or were closed, and records it in
`pull_request_states`, in the same job as for `github.pull_request`. A merge
request that GitLab is merging counts as open. The project of the proposal
must still be one of `limits.projects`; one that is not is skipped. The merged
ones feed the count of noisy alerts fixed. See `github.pull_request` for the
window, the limit and the retention of the records.

Sidekick counts the merge requests it opens in
`sidekick_gitlabmr_merge_requests_total` (by `result`: `opened`, `existing`,
`applied` for a change already on the base branch, `closed` or `reverted` for
an undo, or `error`), the validations in `sidekick_gitlabmr_validations_total`
(by `format` and `result`), and the daily checks in
`sidekick_gitlabmr_status_checks_total` (by `state`: `open`, `merged`,
`closed`, `missing` or `error`).

**What it relies on.** The facts come from the public source and API
documentation of GitLab at the tag `v19.5.1-ee`, and of Gitaly at `v19.5.1`,
as the comments in `internal/action/gitlabmr` say. Nothing was tried against a
GitLab instance. The action sends only these calls: read a branch, a file, a
directory and a blob; list merge requests; make a commit; open a merge
request; close a merge request; delete a branch.

##### `github.revert`

Opens a draft pull request that reverts one commit, or one merged pull
request, when a recent change caused the alert. It is narrower than a code
change: the model names what to revert and why, never the new content of a
file, so a team can allow reverts without allowing arbitrary edits. It is
also how to roll back what Flux, or Argo CD with auto-sync, deploys, since
those put back any change made to the cluster by hand. Use it on a route in
`approved` mode: a person reads the files before anything is pushed.

```yaml
actions:
  revert-api:
    type: github.revert
    app:                              # a GitHub App of its own, not the source's
      appID: 123456
      installationID: 7890123
      privateKeySecret: { name: sidekick-github-revert, key: private-key.pem }
    # baseURL: https://github.example.com/api/v3/   # GitHub Enterprise Server
    limits:
      repos: [acme/api]               # owner/name, no wildcards
      baseBranch: main                # the only branch a revert may target
      maxAge: 72h                     # default 72h, at most 720h
      maxFiles: 10                    # 1 to 25
      maxChangedLines: 300            # lines added plus lines removed, 1 to 1000
      refusedPaths: ["deploy/prod/**"] # added to the built-in list

routes:
  - name: api-errors
    match: { intake: [alertmanager] }
    mode: approved
    actions: [revert-api]
```

- **Credentials.** Give the app its own installation on the repositories in
  `limits.repos`, with these repository permissions: Contents (read and
  write), Pull requests (read and write) and Metadata (read). Do not give it
  Workflows: with that permission missing, GitHub itself refuses a change to
  a workflow file, as a second wall behind the refused paths below. Sidekick
  asks GitHub for installation tokens with just those permissions, and reads
  the private key from its secret on each token exchange, so a rotated key
  needs no restart. `baseURL` must be https, except for a loopback host.
- **Parameters.** `repo`, `baseBranch`, exactly one of `commit` (the full
  40-character SHA, never a branch, a tag or a short SHA) or `pullRequest`
  (the number of a merged pull request), a `title` (the title of the pull
  request) and a `body` (the reason). The description is the model's text,
  with `@mentions` and issue references after words such as "fixes" put in
  code spans so the pull request notifies nobody and closes nothing, then
  the line `Reverts <commit or pull request>` (which GitHub turns into a
  link), a line that marks it as a Sidekick revert, and a line that names the
  proposal and links the investigation. Several commits at once, and
  cherry-picks, are not supported: name one commit, or one pull request.
- **How the revert is made.** GitHub's REST API has no call that reverts a
  commit, so Sidekick makes the revert with the Git data API. It compares the
  tree of the commit with the tree of its first parent (the way `git revert
  -m 1` treats a merge commit). For each file that differs, if the head of
  the base branch still has what the commit wrote, the revert writes what the
  parent had, or deletes the file the commit added, and keeps the file's
  mode. The result is one commit on the branch `sidekick/<proposal id>` and
  a pull request. It is always a draft: there is no `draft` key. Sidekick
  never merges, turns on auto-merge, requests reviewers or assigns anyone.
  If GitHub does not make the pull request a draft, for example in a private
  repository on a plan without drafts, Sidekick closes it, deletes its branch
  and fails the proposal, so a revert is never left open for review.
  There is no three-way merge. If a repeat after a crash finds the branch, and
  the pull request if it was made, it does not make a second one. (GitHub's
  GraphQL API has a `revertPullRequest` mutation that takes a `draft` input,
  but its public schema does not say that an installation token may call it,
  so it is not used.)
- **A pull request by name.** Sidekick finds its merge commit and reverts
  that. A pull request merged with a merge commit is reverted against the
  first parent. A squashed pull request is reverted by its squash commit. A
  pull request merged by rebase has several commits, and GitHub says only
  that its `merge_commit_sha` is the last of them, so it is reverted only
  if Sidekick can show that the commit holds the whole change: the files of
  the pull request must be exactly the files the commit changes, and each of
  them must be the same in the commit's parent as where the pull request
  branched off. A rebase fails this, and so does a squash whose files other
  commits changed on the base branch after the pull request began. The
  refusal tells the model to name the commits one at a time instead.
- **When it is refused.** Each refusal is an error the model reads, and
  nothing is stored. It is refused when the commit is not on the base branch;
  when it is a root commit or has more than two parents; when it landed on
  the base branch longer ago than `maxAge`; when it is a revert made by this
  action (there is no revert of a revert); when it touches a refused path;
  when it changes more than `maxFiles` files or more than `maxChangedLines`
  lines (added plus removed, counted for the revert); and when a file it
  touched is not what the commit wrote any more because someone changed it
  since, with the name of the file and a pointer to `github.code_change` or to
  reverting by hand. It is also refused for what a revert cannot write back:
  a symbolic link, a submodule, a change of file mode, a file that became a
  directory or the reverse, a file larger than 256 KiB, and a file that is
  not UTF-8 text. These checks run when the model proposes and again when
  the proposal runs.
- **Age.** Git does not record when a commit reached a branch, so `maxAge`
  is measured from the time GitHub gives: for a pull request, when it was
  merged; for a commit, its committer date, which GitHub sets when it
  merges, squashes or rebases. A commit pushed long after it was written is
  therefore refused as too old, which is the safe direction. Undo is not
  subject to the age.
- **Refused paths.** The built-in list is always on and cannot be shortened:
  CI and build automation (`.github/**` and the like), ownership and review
  rules (`CODEOWNERS`, Renovate settings), Git settings and hooks
  (`.gitmodules`, `.gitattributes`, hook directories) and secrets and keys
  (`.env` files, private keys, Terraform state and variable files). Paths
  are compared in lower case. `refusedPaths` adds globs to it; `*` and `?`
  do not match a slash, and a segment of `**` matches any number of
  segments. A revert of a commit that touches any of them is refused.
- **What the approver reads.** The proposal shows each file with what
  happens to it (`changed`, `added` or `deleted`) and how many lines the
  revert adds and removes, and the line diff of each file is read from
  GitHub when the approver opens it. Sidekick stores the blob SHAs of the
  files before and after, and the SHA of the base commit, never the file
  contents.
- **Files that changed meanwhile.** When the proposal runs, the action
  compares each file on the base branch with the blob it had when the model
  proposed. A file that someone changed meanwhile fails the proposal with a
  conflict that names the file; propose again. A file that is back to what
  the parent had already is left out, and if every file is, the proposal
  succeeds without a pull request.
- **Result.** The result links the pull request and carries its number and
  the SHA of its head commit.
- **Undo.** An approver can undo a succeeded proposal. If its pull request
  is still open or was closed, Sidekick closes it and deletes its branch. If
  it was merged, Sidekick opens a draft pull request from the branch
  `sidekick/<undo proposal id>` that puts back what the commit wrote, so the
  change the revert took out comes back. It still needs review and merging;
  the undo is done once it is opened. If a file on the base branch is not as
  the revert left it, nothing is written until the approver confirms, and
  then the file is overwritten. This pull request is marked as a revert too,
  so Sidekick will not revert it.
- **It does not fix a noisy monitor.** A revert undoes the cause of an
  alert, so it never counts toward the noisy alerts fixed.

**When to use which.** Use `github.revert` for what Flux, or Argo CD with
auto-sync, deploys: put the earlier files back in Git and the controller
rolls the cluster back. Use [`github.pull_request`](#githubpull_request) to
change a monitor definition, and `github.code_change` to fix forward when
reverting is not enough or a file changed since. Use the rollback actions
for platforms that keep revisions of what they run, such as a Kubernetes
Deployment, a Helm release or an ECS service, where no change in Git is
needed.

Once a day, the same job as for `github.pull_request` reads whether the pull
requests of succeeded proposals are still open, were merged or were closed,
and records it. Sidekick counts the pull requests it opens in
`sidekick_githubrevert_pull_requests_total` (by `result`: `opened`,
`existing`, `applied` for a revert that is on the base branch already,
`closed` or `reverted` for an undo, or `error`) and the daily checks in
`sidekick_githubrevert_status_checks_total` (by `state`: `open`, `merged`,
`closed`, `missing` or `error`).

##### `unleash.flag`

Changes a feature flag in one environment of an Unleash project through the
Unleash Admin API: it turns the flag off, or undoes the newest change of the
flag in that environment. It is approval-only, so a `read-write` route may
list it only when the action sets `allowReadWrite: true`. Use it on routes
with `mode: approved`: the change is live as soon as it is approved. The
flag's other environments are never touched.

```yaml
actions:
  unleash-flag:
    type: unleash.flag
    baseURL: https://unleash.example.com
    tokenSecret: { name: sidekick-unleash-write, key: token }
    limits:
      projects:
        - key: default
          environments: [production]
          flags: ["checkout-*", "new-search"]
      allow: [turn_off]                # default; add restore_previous to allow it

routes:
  - name: checkout
    mode: approved
    actions: [unleash-flag]
```

- `baseURL` is the root of your Unleash instance. It may hold a path, for an
  instance served under one. It must use `https`; plain `http` is accepted
  only for `localhost` and loopback addresses, because the token is sent
  with each request. Required.
- `tokenSecret` is the `{ name, key }` of the Secret that holds the token of
  a service account. Give the action a service account of its own, apart
  from the one of any Unleash source, and give it only the permissions
  listed below. The token is read on every call, so a rotated token needs no
  restart.
- `allowReadWrite` is false unless you set it. See [Action
  modes](#action-modes).
- `limits.projects` lists the projects the action may change, each with its
  `key`, the `environments` in it, and the `flags`: flag names, or patterns
  in which `*` stands for any run of characters, such as `checkout-*`. All
  three are required for every project, so nothing is allowed by default.
  Keys may hold letters, digits, `.`, `_` and `-`.
- `limits.allow` lists the changes the model may propose: `turn_off` and
  `restore_previous`. The default is `turn_off` only, because restoring
  writes more. Listing a change here is how you allow it.

The model proposes a `project`, an `environment`, a `flag` and a `change`.
When it proposes, Sidekick checks them against the limits before anything is
sent, then reads the flag, its strategies and its newest event from Unleash,
and shows the approver the field that changes. It refuses, with a message
the model can act on, a flag that is outside the limits, does not exist in
the project or is archived, or has no such environment.

**`turn_off`** turns the flag off in the environment and leaves its
strategies as they are. It is refused when the flag is already off.

**`restore_previous`** undoes the newest change of the flag in that
environment, and only that one. Unleash records each change as an event that
holds that one change, not the whole environment, so Sidekick can restore
exactly these:

- turning the flag on (the restore turns it off) or off (the restore turns
  it on);
- changing a strategy (it gets the values it had before, all of them:
  constraints, parameters, variants, segments, title, sort order and
  whether it is disabled);
- adding a strategy (it is removed);
- removing a strategy (it is added again, with a new ID).

It is one call to Unleash. It is refused, with the reason, when the newest
change is anything else, such as reordering the strategies or a change that
is part of a group of changes made together (a change request); when the
flag has a strategy that belongs to a release plan; when the strategy it
would change is gone, or already has its earlier values; and when the result
would leave the flag on with no enabled strategy, because Unleash then turns
the flag off, or adds a default strategy, by itself. Use `turn_off`, or
change it in Unleash, for those.

**What the token needs.** In the Unleash UI, create a service account for
Sidekick and give it a custom project role with only these permissions, on
the allowed projects and, where Unleash lets you, only the allowed
environments:

- `UPDATE_FEATURE_ENVIRONMENT`, for `turn_off`, for the turn on and off of
  `restore_previous`, and for undoing either;
- `CREATE_FEATURE_STRATEGY`, `UPDATE_FEATURE_STRATEGY` and
  `DELETE_FEATURE_STRATEGY`, only when `restore_previous` is allowed.

The action also reads the flag, its strategies and its events, which any
signed-in service account may do, so a root role with no write permissions,
such as Viewer, is enough besides the project role. Do not give the account
`SKIP_CHANGE_REQUEST` or an admin role. Do not use an `ADMIN` API token or
a client token.

**Change requests.** An environment that uses change requests refuses a
direct change from an account that cannot skip them, and Sidekick never
opens a change request. Unleash does not say which environments use them
through the public API, so Sidekick finds out only when the approved change
runs: the proposal then fails, with a message that says so, and nothing is
changed. Make the change in Unleash with a change request instead.

**When it runs.** Right before it writes, Sidekick reads the flag again.
The version of a flag is the ID of its newest event in the environment, so
the proposal fails with a conflict when someone changed the flag since, or
when its strategies are not as they were when it was proposed. Unleash has
no precondition on writes, so a change made in the moment between that read
and the write is not noticed. When the flag already has the new state, which
happens when a worker died after the write, it writes nothing and succeeds.

**Undo.** An approver can undo a change that worked. Undo makes the one call
that reverses it: it turns the flag back on or off, puts a strategy back,
removes the strategy that was added, or adds the one that was removed. It
looks only at the part the action changed. If that part is not as the action
left it, Undo writes nothing and shows the flag as it is, and asks again
with `force`. A strategy that the action added has no ID in the proposal, so
Undo takes the strategy that is new since the change; if there are several
new ones and Sidekick cannot tell which is it, Undo fails and says so.

The calls were checked against the source of Unleash 8.2.1.

##### `helm.rollback`

Puts a Helm release back on an earlier revision, with Helm's own Go library
(the code that `helm rollback` runs, not the `helm` program), in the cluster
Sidekick runs in. Helm makes a new revision that holds the chart, values and
manifests of the earlier one. Use it for a release that Helm manages and that
no GitOps controller owns: Argo CD or Flux would undo the change at their next
sync. It is approval-only: a route in `read-write` mode may list it only when
the action sets `allowReadWrite: true`. Use it on routes with `mode: approved`.
It does not wait for the alert to recover: the watcher reports that.

```yaml
actions:
  helm-checkout:
    type: helm.rollback
    serviceAccount: sidekick-helm-rollback   # default; the account the tokens are for
    maxRevisionsBack: 1                      # default 1; from 1 to 10
    wait: true                               # default; wait until the objects are ready
    waitTimeout: 3m                          # default; above 0 and at most 4m
    driver: secret                           # default; or configmap
    limits:
      namespaces: [shop]                     # namespaces it may roll back in
      releases: [checkout, "web-*"]          # release names; * stands for any run of characters

routes:
  - name: checkout
    mode: approved
    actions: [helm-checkout]
```

- `limits.namespaces` and `limits.releases` are required. A release is
  allowed when its namespace is in `namespaces` and its name matches an entry
  of `releases`. An entry is an exact name or a pattern in which `*` is the
  only wildcard.
- `maxRevisionsBack` is how far back the model may ask to go, and the
  parameter `revisionsBack` (default 1) may not be larger. It sits next to
  `allowReadWrite` and not under `limits`, as in the other rollback actions.
- `wait` and `waitTimeout`: with `wait: true` Helm waits until the objects are
  ready, for at most `waitTimeout`. Helm uses the timeout for the hooks and for
  the wait one after the other, and Sidekick cuts a run off after 5 minutes,
  so `waitTimeout` is at most 4 minutes. If the wait fails or times out, Helm
  records the new revision as `failed` and the proposal fails. With
  `wait: false` only the hooks are waited for. An Undo never waits for the
  objects, because the person who asked for it waits for the answer for at
  most a minute.
- `driver` is where Helm keeps the release records of your releases: `secret`
  (Helm's default) or `configmap`. It must match how the releases were made.
- There are no keys for `force`, `recreate` or the control of hooks. They keep
  Helm's defaults, so the hooks of the chart run (`pre-rollback` and
  `post-rollback`), and a rollback never replaces objects by force.
- Sidekick removes no revision. Helm's own history limit applies when you
  upgrade.
- The action works on the cluster Sidekick runs in only.

**Which revision.** Sidekick reads the history of the release.

- The revision that runs is the newest one, when its status is `deployed` or
  `failed`. A failed upgrade leaves the revision before it as `deployed` in
  Helm's records, although the objects in the cluster are the failed
  revision's, so Sidekick counts from the newest revision, as `helm rollback`
  does.
- "N back" counts the earlier revisions with status `superseded` or
  `deployed`. Revisions that are `failed` or `pending-*` are skipped.
- It is refused, with a message the model can read, when the release is not
  found, when its newest revision is `pending-*` (another operation runs on it
  or was interrupted), when its newest revision has another status such as
  `uninstalled`, and when fewer earlier revisions than `revisionsBack` can be
  rolled back to.

**What the approver sees.** Both revisions: the Helm revision number, the
chart name and version and the app version, the time Helm last deployed it, and
the description Helm kept. Helm does not record who deployed a revision, so that
shows as "unknown" and the notes say so.

**The credential is broad, and Sidekick does not grant it.** A Helm rollback
re-applies the manifests of the target revision, so the account needs rights
on every kind of object in the chart, in the namespace of the release, and
on the release records. Kubernetes RBAC cannot narrow that to one release. The
chart makes the service account (with `serviceAccount` of the action, and
`sidekick-helm-rollback` when it is not set), and a Role in Sidekick's namespace
that lets Sidekick's own account ask for tokens of it and of no other account.
It makes no Role or binding for the account in the namespaces of the releases.
You bind one yourself, in each namespace of `limits.namespaces`. The simplest is
the built-in `edit` ClusterRole:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: sidekick-helm-rollback
  namespace: shop
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: edit
subjects:
  - kind: ServiceAccount
    name: sidekick-helm-rollback
    namespace: sidekick   # the namespace Sidekick runs in
```

Read what this allows before you apply it. `edit` lets the account create,
change and delete most objects in that namespace (workloads, Services,
ConfigMaps, Secrets, Ingresses and more) and read every Secret in it. The
`limits` of the action are a second layer that stops the model; they do not
change what the account may do, so anyone who can get a token for it can do all
of that. The `edit` role does not cover Roles and RoleBindings (that is
`admin`), custom resources, or objects that are not in a namespace. A chart
that has any of those needs a Role of your own.
If you write your own Role, a rollback needs, in the namespace of the release:

- `get`, `create`, `patch` and `delete` on every kind of object that the old or
  the new revision has, because Helm reads each object, creates the missing
  ones, patches the others and deletes those the target revision does not have;
- `list`, `get`, `create` and `update` on `secrets` (with `driver: configmap`,
  on `configmaps`), for the release records, which are named
  `sh.helm.release.v1.<release>.v<revision>`; the list is by label, so it
  cannot be limited by name;
- `list` and `watch` on every kind of object in the chart, when `wait` is on;
- `create` and `delete` on the objects of the hooks of the chart.

**Conflicts and retries.** The snapshot holds the revision that runs. When the
proposal runs, Sidekick reads the history again. If the newest revision is not
the one of the proposal (someone deployed, rolled back, or an upgrade failed),
it fails as a conflict and a new proposal is needed. Helm stores each revision
under its own key and refuses to write one that exists, so two operations that
start at the same revision cannot both make the next one. If the rollback is in
place already (a worker died after Helm made it), the proposal succeeds and
changes nothing, so running it twice makes one new revision. A rollback that
Helm started and that did not end as `deployed` is not repeated, because that
would make more revisions: the proposal fails and the message says what the
newest revision is. If Helm was interrupted, for example by a restart, the new
revision stays `pending-rollback` and Helm refuses other operations on the
release until a person checks it. Look at the release with Helm's tools, and when
no operation runs, delete the `Secret` (or `ConfigMap`) named in the message,
`sh.helm.release.v1.<release>.v<revision>`, then propose again if it is still
needed.

**Undo.** Undo rolls back to the revision that ran before the rollback, which
Helm makes a new revision again. The proposal shows the two revisions the other
way round. If someone deployed or rolled back since, Undo shows the revision that
runs now and asks before it rolls back anyway. Undo that is repeated changes
nothing once the release is restored.

**What it relies on.** Helm v4.3.1 (the `pkg/action` functions `History` and
`Rollback`) and the built-in `edit` role of Kubernetes v1.37.1. The comments in
`internal/action/helmrollback` name the files. With `networkPolicy.restrictEgress`
on, allow egress to the Kubernetes API server.

### `validator`

How Sidekick reaches the [monitor validator](validator.md), which checks
the files of a pull request action before the pull request opens. The
chart's `validator` values set the validator up; this section only says
where Sidekick finds it.

```yaml
validator:
  mode: sidecar                  # sidecar (the default) or job
  url: http://127.0.0.1:8200     # where the sidecar listens; this is the default
  allowDownloads: false          # the chart's setting of the same name; job mode passes it to the Jobs
```

With `mode: sidecar`, the default `url` is the address of the chart's
sidecar, so with `validator.enabled: true` in the chart values there is
nothing to set here. An action that needs the validator reports an error
when it cannot reach it; nothing else in Sidekick uses it.

With `mode: job`, Sidekick runs each validation as a Kubernetes Job in its
own namespace, and `url` is not used. In the Helm chart, set `validator.mode:
job` in the chart values, not here: the chart then writes this section,
including the `job` block below, from its own `validator` values, and
its values win over anything in `config.validator`. Outside the chart, set
the block yourself:

```yaml
validator:
  mode: job
  allowDownloads: false
  job:
    image: ghcr.io/nourtalaat/sidekick-validator:0.2.0   # required
    imagePullPolicy: IfNotPresent    # Always, IfNotPresent or Never; empty is the cluster's default
    imagePullSecrets: [registry-login]
    serviceAccountName: sidekick-validator   # not Sidekick's own; the pod mounts no token
    resources:
      requests: {cpu: 100m, memory: 256Mi}
      limits: {memory: 1Gi}
    workDirSizeLimit: 2Gi            # size limit of the directory for the files; empty is none
    podLabels:                       # on the Jobs, their pods and ConfigMaps
      app.kubernetes.io/component: validator
```

Sidekick needs permission to create, get and delete Jobs, create and delete
ConfigMaps, and list pods, in its own namespace. It starts without calling
the cluster, and uses the API on the first validation, so `config validate`
works outside a cluster. Job mode needs Sidekick to run in a cluster, with its
service account token mounted. The image must be of the same release as
Sidekick. How a validation runs, its time limits and the limits of job
mode are in [The monitor validator](validator.md#job-mode).

### `strategies`

A strategy tells an investigation what to check first, the usual causes and
the signs of a false positive for one kind of event. Each route names one.

```yaml
strategies:
  dir: /etc/sidekick/strategies   # extra strategy files, read-only
```

Sidekick ships with these built-in strategies:

| Strategy | For |
|---|---|
| `generic-alert` | Any alert or event that no more specific strategy covers. |
| `latency-alert` | A latency monitor, such as p95 or p99 response time, for an HTTP or RPC service. |
| `error-rate-alert` | An error rate monitor, such as the share of failed requests or 5xx responses. |
| `service-down` | A service, host or endpoint that stopped answering, such as a failed health check or uptime probe. |

`dir` adds your own: every `*.yaml` or `*.yml` file at
the top of the directory is one strategy. Hidden files and other files are
skipped. A `dir` that is not set, or does not exist, adds none; one that
exists but cannot be read stops Sidekick from starting.

- Sidekick only reads these files. To change a strategy, change its file,
  for example through version control and a new deploy, and restart.
- A file strategy with the name of a built-in one replaces it, and start-up
  logs a line saying which one.
- Each strategy's version is a hash of its file content, so every
  investigation records the exact strategy text it used.

#### Strategies saved at runtime

An admin can also save a strategy in the [web UI](web-ui.md#strategies) or
through the API. It is stored in the database, and every save is a new
version with its author and a required reason. A stored version is never
changed or deleted.

- The three kinds are layered by name: a runtime strategy replaces the
  built-in strategy of the same name, and a strategy file replaces both.
  Editing a built-in strategy in the UI saves a runtime strategy with its
  name. A runtime strategy whose name a strategy file has is shown as
  shadowed, and is used again if the file is removed.
- A runtime strategy goes through the same checks as a strategy file, and
  a save that fails them is refused with the path and line of each
  problem.
- The version of a runtime strategy is written `runtime:N`, where N is its
  version number, so it cannot be mistaken for the hash of a file. An
  investigation records the version it started with and keeps it: a
  version saved later is used by investigations that start after it, and
  a follow-up uses the text the first run had.
- Every replica uses a saved version within seconds. Routes that name the
  strategy, in the file or at runtime, use its newest version for the
  investigations they start from then on.
- A route in the config file names a strategy that is built in or in a
  strategy file, and so does a route added at runtime. A strategy that
  exists only at runtime is used when triage picks it, and when it has the
  name of a built-in strategy, which replaces that one wherever it is
  named.

A strategy file looks like this. Unknown keys are errors, as in
`sidekick.yaml`.

```yaml
name: latency-alert                # required; lower-case letters, digits and "-"
description: A latency monitor fired for an HTTP or RPC service.   # required
match:                             # the events the strategy fits; triage is
  alertNames: ["*Latency*"]        # told which strategies fit an event
  textPattern: "(?i)latency|slow"  # a regular expression
sources:
  prefer: [signoz, k8s]            # sources to try first; unknown names are skipped
first:                             # the checks to run first, in order
  - Check what changed in the 2 hours before the alert started.
  - Compare latency with request rate.
commonCauses:
  - A deploy that added a slow query.
falsePositiveSigns:
  - Very low request volume during the window.
verdictHints:                      # also truePositive, inconclusive, duplicate,
  falsePositive: Propose a minimum request rate condition.   # knownIssue, flapping
budgets:                           # where the route sets one too, the lower
  maxToolCalls: 30                 # of the two limits applies; also
  maxWallTime: 5m                  # maxTokens and maxCostUSD
```

A strategy can also define `watchers` and `watcherDefaults`, written as on
a route (see [Watchers](#watchers)). A strategy's watchers start for the
groups of every route that uses the strategy, and its `watcherDefaults`
apply to them and to the watchers the model starts during its
investigations. They are checked with the limits of each route that names
the strategy, and a problem is reported at that route's `strategy` field.

#### Proposed changes

```yaml
strategies:
  proposals:
    enabled: false        # default false
    wrongThreshold: 3     # default 3
    window: 14d           # default 14d
```

When people keep rating one version of a strategy wrong, Sidekick can draft a
change to it and check the change in the benchmark before anyone applies it.
This is off unless `enabled` is `true`. Off, Sidekick counts nothing, drafts
nothing, runs no gate and spends nothing.

- **The trigger.** A strategy version that has `wrongThreshold` ratings of
  wrong on a verdict or a finding within `window`, counted on investigations
  that ran that exact version, gets a proposal. Each rating of one person on
  one part counts once. A version gets one proposal, ever, however many wrong
  ratings follow.
- **The draft.** The model of the `summarize` role gets the strategy, the
  wrong ratings with their reasons, the confirmed investigations of the
  strategy and the lessons already in use for it, and answers with the whole
  changed strategy and a short reason. The text must pass the checks of a
  saved strategy, keep the name and differ from the current text; the model
  gets one more try with the problems when it does not. A draft that still
  fails, or a strategy that has a newer version by then, ends the proposal as
  `draft_failed` with the reason, and that version gets no other proposal.
  Without a `summarize` role, no proposal is drafted and Sidekick logs that
  once at start-up.
- **The gate.** A drafted change goes through the [benchmark
  gate](benchmark.md#the-strategy-gate) at once. Only a change that passed it
  can be approved, in the [review queue](web-ui.md#the-review-queue).

Turning proposals on costs money. Each proposal is one `summarize` call, and
each gate runs every scenario that covers the strategy `bench.gate.minRepeats`
times with the current version and again with the change, so at least
`2 x minRepeats x (the number of scenarios)` investigations, plus the judge's
grading of each. Every call is priced and counted in the daily spend and the
daily caps. [`bench.gate.maxCostUSD`](#bench) caps what one gate may spend.

### `routes`

Routes decide how an event is investigated and where the report goes. They
are tried in order of `priority`, lower first, then by name, and the first
route that matches wins. A route named `default`, with no `match`, is
required; it takes every event no other route matches and is always tried
last.

```yaml
routes:
  - name: payments-latency
    priority: 10                       # default 100
    match:                             # every field that is set must match
      intake: [alertmanager]           # webhook endpoint or listener names, slack-mention or teams-mention
      labels: { team: payments }
      service: "checkout-*"            # a glob
      severity: [warning, critical]    # unknown, info, warning or critical
      text: "(?i)latency|p99"          # a regular expression
    strategy: latency-alert
    roles: { investigate: claude }     # overrides model.roles for this route
    sources: [signoz]
    mode: approved                     # read-only (default), approved or read-write
    actions: [monitor-note]            # actions the model may propose, defined in `actions`
    outputs: [slack-thread]
    details:                           # facts the model is told, as trusted text
      owner: payments team
      environment: prod
      runbook: payments-latency page in the on-call wiki
    budgets:                           # at or below the global budgets
      maxToolCalls: 30
      maxCostUSD: 0.75
      daily: { maxCostUSD: 40, onDailyCap: skip }   # this route's daily cap
    concurrency: 3                     # at most 3 of its investigations at once
    rateLimit: { events: 60, per: 1h, overLimit: delay }   # at most 60 events an hour
    grouping:
      window: 30m                      # default 30m
      fingerprintLabels: [alertname, service, env]
      relatedWindow: 10m               # default 0, which is off
  - name: default
    strategy: generic-alert
    outputs: [slack-thread]
```

A route without `strategy` uses the built-in `generic-alert` strategy.

`details` gives the model fixed facts about the route, such as the owning
team, the environment or the runbook to follow. Each one is shown as a
`key: value` line under `Route:` in the first request of an investigation
and in its triage request, sorted by key. The limits are:

- at most 20 keys;
- a key is 1 to 64 letters, digits, `-`, `_` or `.`;
- a value is not empty, is one line of at most 500 characters, and holds no
  control character, so no line break or tab.

A route that breaks a limit fails config load, and a runtime route that
breaks one is refused with a message that names the key. Details are shown
to the model as trusted facts, not inside the block of untrusted data that
holds the alert, so they must hold no secrets and no text copied from
alerts or other outside sources. They are saved with the investigation's
prompt, which anyone who can open the investigation can read, and they go
to the model provider. The model does not write links, so a link in a
detail is not passed on to reports. Because the text is trusted, only the
config file, or an admin through the web UI or the API, can set details;
Slack commands cannot, and `/sidekick routes` lists only the keys.

Events from mentions of `@Sidekick` have the intake `slack-mention`, or
`teams-mention` in Teams.

The match runs on the event as it is stored, after
[redaction](#redaction). `text` is matched against the event's title and
text, joined by a newline, so `^` and `$` mark the start and end of the
whole text unless the pattern turns on multi-line mode with `(?m)`.
`service` is matched as a whole, so `checkout-*` matches `checkout-api` but
not `eu-checkout-api`. A label in `labels` must be present with exactly
that value.

Each routed event opens an [investigation](investigations.md) with the
route's strategy, the connector and model for each role (the route's
`roles` over `model.roles`), and its budgets. Where the strategy sets a
budget too, the lower of the two applies. These settings are fixed when
the investigation is created, so a config change does not alter one that
has started. An investigation whose route, connector or strategy is no
longer in the config when it starts or resumes ends as "Could not
finish". `sources` limits the tools the investigation may call to those
sources; without it, every source's tools are offered.

#### Grouping

Events that are the same alert join one group, and a group has one
investigation and one Slack message, so 30 alerts with the same fingerprint
make one investigation, not 30. An event joins the open group with its
fingerprint; when there is none, it opens a new group and a new
investigation. A joined event adds 1 to the event count the Slack message
shows, and the running investigation sees it before the model's next turn.

The fingerprint of an alert is made from the intake, the alert name and the
values of the labels in `grouping.fingerprintLabels`. A label the alert
does not have counts as empty. Without `fingerprintLabels`, or when the
alert has none of the listed labels, the fingerprint is the one the intake
gave the alert, which for Alertmanager and the generic adapter covers all
of its labels, so only repeats of the very same alert join. Messages and
mentions always use the intake's own fingerprint.

A group stays open for `grouping.window` after its newest event. When no
event has arrived for that long, it closes as `window_ended`, and the next
event with the same fingerprint opens a new group and a new investigation.
A group also closes, as `resolved`, once every alert in it has recovered.
A group with a running [watcher](#watchers) stays open after its window
and closes when its last watcher ends.

With `grouping.relatedWindow`, alerts of related services join one group
too. An event that finds no open group with its fingerprint joins the
route's open group of the same service, or of a service that it depends on
or that depends on it, when that group's newest event is at most
`relatedWindow` old. If several groups fit, it joins the one with the
newest event. The running investigation sees the event, with its service,
before the model's next turn. The group keeps the fingerprint of the alert
that opened it, so later events with the joined alert's fingerprint join
it only while it stays related and recent. Only events of the same route
join, and only direct dependencies count, not dependencies of
dependencies. Dependencies come from the [service catalog
sources](sources/backstage.md); without one, only alerts of the same
service join. The default, `0`, turns related grouping off; `10m` is a good
start. It must not be longer than `window`.

Recovery events, such as a resolved notification from Alertmanager, are not
routed. Each one is linked to the newest group that has the same alert,
and the investigation sees it before the model's next turn, as a sign that
the alert may have cleared on its own. A recovery never stops a running
investigation. A recovery whose alert is in no group is stored as skipped.

#### Watchers

A watcher is a periodic, read-only check that tells when a service is back
up after an alert. A route's `watchers` start when one of its groups opens
for an event that matches them, and each runs its first check at once. See
[`watch`](#watch) for the global settings and the check types.

```yaml
routes:
  - name: payments-latency
    # ...
    watcherDefaults:                   # defaults for this route's watchers,
      interval: 20s                    # including those the model starts
      timeout: 90m
    watchers:
      - name: checkout-health          # unique within the route
        match: { alertName: ["*Down*", "*Unavailable*"] }   # globs; empty matches every event
        check:
          type: http
          url: https://checkout.internal.example.com/healthz
          expect: { status: [200], bodyRegex: '"status":\s*"ok"' }
        interval: 15s                  # a watcher's own values win
        passesToRecover: 3
      - name: checkout-error-rate
        match: { text: "(?i)error rate" }   # a regular expression on the event text
        check:
          type: tool
          source: signoz               # one of the route's sources
          tool: signoz_query_metrics   # the tool's name as the source names it
          args: { query: "error_rate{service=\"checkout-api\"}", window: 5m }
          condition: { jsonPath: "$.series[0].last", op: lt, value: 0.01 }
      - name: alert-cleared
        check: { type: alert, require: all }
    watcherLimits:                     # what the start_watcher tool may use here
      allowModel: true                 # false turns start_watcher off for this route
      types: [http, tool]              # default: every type
      tools: ["signoz__signoz_query_metrics"]   # qualified names; default: none
      hosts: ["*.internal.example.com"]   # default: watch.http.allowHosts
      minInterval: 15s                 # route bounds, inside the global ones
      maxInterval: 5m
      maxTimeout: 2h
```

Each setting of a watcher comes from the first of these that sets it: the
watcher itself, the strategy's `watcherDefaults`, the route's
`watcherDefaults`, then `watch.defaults`. The chosen value must be inside
the route's `watcherLimits` bounds and the global bounds. The values are
copied into the watcher when it starts, so a change to defaults or bounds
applies to new watchers only.

A route without `watcherLimits` lets the model start watchers of every
check type, with no tool checks (the tool list is empty) and HTTP checks
only to the hosts in `watch.http.allowHosts`.

#### Action modes

A route's `mode` is `read-only`, `approved` or `read-write`. `read-only` is
the default: the model has no way to propose a change, and fixes appear only
as text in the report. In `approved` mode the model may propose the route's
`actions` and a person approves or rejects each proposal; see
[`actions`](#actions). In `read-write` mode the model proposes the same way,
but a proposal that passes its checks is approved by the route's policy and
runs at once, with no person involved. The audit log records that approval
with the actor `policy`, and Slack and the web UI show "approved by policy
(read + write route)". `actions` lists names from the `actions` section,
and must be empty in `read-only` mode and have at least one name in
`approved` and `read-write` mode.

The mode is fixed when the investigation is created, like the other
settings above. The route's `actions` are read again when the model
proposes and when a proposal runs; see [`actions`](#actions).

Some actions change a running system, for example a rollback. Such an
action is approval-only: a `read-write` route may list it only when its
definition in `actions` sets `allowReadWrite: true`. Without the key, the
file is refused, and so is a route that an admin saves in the web UI or the
API. Use `mode: approved` for the route, so a person approves each
proposal, or set the key to let the policy approve them. The key is an
unknown-key error for an action type that is not approval-only. Which
action types are approval-only is said in their own section.

```yaml
actions:
  fix-api:                          # an approval-only action type (an example)
    type: example.rollback
    tokenSecret: { name: rollbacks, key: token }

routes:
  - name: api-fixes
    match: { intake: [alertmanager] }
    mode: read-write
    actions: [fix-api]              # refused
```

```text
routes[0].actions[0]: action "fix-api" (type example.rollback) is approval-only, so a read-write route may not list it as it is; use mode: approved on this route, or set allowReadWrite: true on the action in the config file
```

Either fix makes the file load:

```yaml
actions:
  fix-api:
    type: example.rollback
    tokenSecret: { name: rollbacks, key: token }
    allowReadWrite: true            # the policy approves its proposals with no person

routes:
  - name: api-fixes
    match: { intake: [alertmanager] }
    mode: read-write
    actions: [fix-api]
```

or keep the action as it is and set `mode: approved` on the route.

Sidekick logs a warning for each `read-write` route and action with
`allowReadWrite: true`, every time it starts. If the key is missing when
the model proposes, for example because the config changed after the route
was loaded, the proposal is not approved by the policy: it waits for a
person, and the model is told so. A proposal that the policy approved is
refused when it runs if the action has become approval-only without the
key since. A proposal that a person approved is not affected by the key.

#### Read + write mode and trusted senders

Read + write mode runs changes on the strength of an alert, and the text of
an alert comes from outside. So it is limited to senders you control:

- A webhook endpoint or listener is trusted only with `trusted: true`. A
  listener also needs the bots in `slack.trustedBots` or `teams.trustedBots`,
  and only a bot message from them counts as trusted; a message from a
  person in that listener is not. Runtime listeners and mentions of Sidekick
  (`slack-mention`, `teams-mention`) are never trusted.
- A `read-write` route whose `match` can select an intake or listener that
  is not trusted is a config error. `match.intake` is what the route can
  select; a route with no `intake` in its match can select every intake and
  listener, including mentions when the `slack` or `teams` section exists.
  The `default` route has no match, so it can be `read-write` only when
  every intake is trusted. The same check runs when an admin saves a route
  in the web UI or the API.
- At run time an event that is not trusted never gets read + write mode:
  its investigation runs in `approved` mode and its proposals wait for a
  person. The same happens to a `read-write` investigation when an event
  that is not trusted joins it later, by the same fingerprint, through
  `grouping.relatedWindow`, or as a recovery event: it becomes `approved`
  from then on, because the text of that event reaches the model. A
  follow-up question does the same, from Slack or the web UI, because it
  comes from whoever is in the thread or signed in and its text steers the
  model. `allowUntrustedWrite` does not change this: it is about the senders
  the config names.
- Only the config file and an admin in the web UI or the API can set
  `read-write`. Slack commands cannot.

```yaml
routes:
  - name: payments-notes
    match: { intake: [alertmanager] }  # alertmanager sets trusted: true
    mode: read-write
    actions: [monitor-note]
  - name: ci-notes
    match: { intake: [generic-ci] }    # not trusted
    mode: read-write
    allowUntrustedWrite: true          # the explicit override; without it the file is refused
    actions: [monitor-note]
```

`allowUntrustedWrite: true` lifts both limits for one route: it may select
senders that are not trusted, and their events keep `read-write` mode. It
does not keep that mode after a follow-up question.
Sidekick logs a warning for each such route, from the file or saved in the
web UI, every time it starts. A runtime route saved with it is in the audit
log like every route change, with the old and new values. In the web UI the
admin must confirm the override before the route is saved. Use it only when
something else already guards the sender, for example a network policy in
front of the endpoint.

### `budgets`

The global limits for one investigation. They are the defaults for routes
and the ceilings route budgets must stay within.

```yaml
budgets:
  perInvestigation: { maxTokens: 400000, maxToolCalls: 40, maxWallTime: 10m, maxCostUSD: 1.00 }
  daily: { maxCostUSD: 200, timeZone: UTC, onDailyCap: wait }   # or skip
  concurrency: 8
  rateLimit: { events: 600, per: 1h, overLimit: delay }          # or skip
```

`maxTokens` counts the input tokens, including those read from or written
to the prompt cache, and the output tokens of every model call.
`maxToolCalls` counts calls to source tools. `maxWallTime` counts from when
the investigation first started, including time lost to a restart.
`maxCostUSD` is the cost of the model calls, in US dollars, from the
[price table](#model). Near any of them, the model gets one last turn to
report what it found; an investigation that runs out ends as "Not sure yet"
with what it found. See [Limits](investigations.md#limits). Each
[follow-up run](investigations.md#follow-up-questions) gets half of each
of the investigation's limits; there is no key to change that yet.

#### Daily caps

`daily.maxCostUSD` caps what every investigation together may cost in one
day, and a route's own `budgets.daily.maxCostUSD` caps that route's share,
at or below the global cap. Neither is set by default. The day starts at
midnight in `daily.timeZone`, an IANA name such as `UTC` or
`Europe/Berlin`; it is the same for every cap, and routes cannot set their
own.

Once a cap is reached, `onDailyCap` decides what happens to investigations
that have not started yet:

- `wait` (the default) holds them until the next day starts, then runs
  them.
- `skip` ends them at once as "Not sure yet", with the cap as the reason.

A route's `onDailyCap` applies at its own cap; the global one applies at
the global cap and on routes that set none. Either way, Sidekick posts one
notice a day to the route's outputs that can take one, such as the
channel of a Slack output. An investigation that is already running when
a cap is reached is not stopped at once: the model gets its last turn and
the investigation ends as for a budget.

#### Rate limits

`rateLimit` limits how many events are routed in any window of `per`,
counted when each event is routed. A route's own `rateLimit` counts that
route's events and must not allow more events per second than the global
one. An event over a limit is:

- `delay` (the default): marked `delayed` and routed again once the window
  has room, so it is investigated later.
- `skip`: marked `skipped` with the reason, and never investigated.

Each limit posts at most one notice per window to the route's outputs that
can take one. Recovery events are not counted.

`concurrency` is how many investigations run at once, counted across every
replica. A route's own `concurrency` caps that route's share of them and
must be at or below the global value; a route without one is held only by
the global limit. An investigation over a limit waits in the queue until
one ends.

### `report`

```yaml
report:
  minFalsePositiveConfidence: high   # low, medium or high; default high
```

A false positive verdict below this confidence is not accepted. Lower it
only if you accept that a real incident may be reported as a false positive
more often.

### `memory`

```yaml
memory:
  enabled: true   # default true
  examples: 3     # 1 to 10; default 3
```

Sidekick remembers every investigation whose verdict a responder marked
correct, and shows the model up to `examples` of them that look like the
alert it investigates, as hints about where to look. It picks those with
the same fingerprint first, then those with the same service and alert
name, then those whose summary shares words with the alert. A Wrong that
takes the confirmation back takes the investigation out of memory again,
so investigations nobody confirmed are never shown. With
`enabled: false`, prompts show none. Entries are kept for
[`retention.memory`](#retention), 730 days by default, even after their
investigation is deleted. To let the model search memory itself, add a
[memory source](sources/memory.md).

#### Vector memory (`memory.vector`)

```yaml
memory:
  vector:
    enabled: true      # default false
    embeddings:        # required when enabled
      provider: openai # openai, bedrock or voyage
      model: text-embedding-3-small
      dimensions: 1536
      apiKeySecret: { name: sidekick-openai, key: apiKey }
```

`memory.vector` is optional and off by default. Turned on, it embeds memory
entries with an embeddings model, so a search can find entries that mean
the same thing as the alert in words the full-text search does not match.
`embeddings` is a model connector section of its own, with the keys of the
provider it names and `dimensions`, the length of the vectors the model
returns. `dimensions` is valid only here: a connector under
`model.connectors` that sets it, or that names an embedding model, is a
configuration error. Pgvector indexes vectors of at most 2,000 dimensions,
so `dimensions` is at most 2000.

Sidekick checks the model and `dimensions` against its table of known
embedding models when it reads the file, and every vector the provider
returns against `dimensions` on each call, so a wrong value fails with a
message that names the setting. The `retries`, `timeout` and
`baseURL` rules of the other connectors apply. The texts sent are memory
entries, which are redacted already, and are cut to 8,000 bytes, at most 64
to a call.

Vector memory needs a Postgres with the
[pgvector](https://github.com/pgvector/pgvector) extension, version 0.5 or
newer, which has the HNSW index. With `enabled: true`, `sidekick serve`
(unless `database.migrations` is `manual`) and `sidekick migrate up` run a
second set of migrations, which creates the extension, the
`memory_embeddings` table, the column with the length `dimensions` gives
and an HNSW index by cosine distance. The role that migrates must be allowed
to create the extension; managed Postgres services usually let you enable
it once, and Amazon RDS, Cloud SQL, Azure Database for PostgreSQL and
Supabase all offer it. Sidekick keeps that set's progress in its own table,
`goose_db_version_vector`. With vector memory off, the extension, the table
and the queries are never touched, so any Postgres 15 or newer works.

Changing `dimensions` later makes Sidekick refuse to start until you run
`sidekick migrate vector-reset`, which empties the vectors so they are made
again; see [the memory source](sources/memory.md#changing-the-length-of-the-vectors).
Changing only the model, to one with the same `dimensions`, needs no reset:
the start-up backfill embeds the entries again. Searches compare vectors of
the current model only, so until an entry is embedded again by the new
model, it is found by full text alone. The Postgres that the
bundled chart runs does not have pgvector; see `postgres.image` in the
chart's `values.yaml` for an image that does.

| Provider | Models | `dimensions` | Notes |
|---|---|---|---|
| `openai` | `text-embedding-3-small`, `text-embedding-3-large`, `text-embedding-ada-002` | Up to 1536 or 3072 for the `3` models, which Sidekick asks the API to shorten; 1536 for `ada-002` | `apiKeySecret`, or `baseURL` for an OpenAI-compatible server such as Ollama or vLLM. For a model Sidekick does not know, set `dimensions` to what the server returns; Sidekick does not ask the server to shorten the vectors. |
| `bedrock` | `amazon.titan-embed-text-v2:0`, `cohere.embed-v4:0`, and their `us.`, `eu.` and other geo inference profile IDs | 256, 512 or 1024 for Titan; 256, 512, 1024 or 1536 for Cohere | `region`, and the same AWS credentials as a Bedrock generation connector, which also need `bedrock:InvokeModel`. |
| `voyage` | `voyage-4-large`, `voyage-4`, `voyage-4-lite`, `voyage-3-large`, `voyage-3.5`, `voyage-3.5-lite`, `voyage-code-3` | 256, 512, 1024 or 2048 | `apiKeySecret` is required. `baseURL` replaces `https://api.voyageai.com/v1`; it must use https, except for a loopback host. |

Settings that only mean something for generation, such as `maxTokens`,
`temperature`, `thinking`, `effort`, `api` and `capabilities`, are errors
in `embeddings`. Anthropic and Google connectors cannot embed.

Embedding calls are counted in `sidekick_model_calls_total` with `role="embed"`,
and their input tokens and cost in `sidekick_model_tokens_total` and
`sidekick_model_cost_usd_total`. The cost comes from the shipped price
table, which has the models above (input tokens only); `sidekick doctor`
warns about an embedding model that has no price, and `model.prices` adds
one. Cohere on Bedrock does not report tokens in its answer, so Sidekick
reads them from a response header when Bedrock sends it, and the cost shows
zero when it does not.

### `lessons`

```yaml
lessons:
  requireApproval: true   # default true
  maxPerPrompt: 10        # 1 to 50; default 10
  maxTokens: 1500         # 200 to 20000; default 1500
  globalShare: 0.3        # 0 to 1; default 0.3
```

A lesson is a short piece of guidance that people reviewed, up to 400
characters, with a scope: a service, an alert name, a route or a strategy.
A lesson applies to an investigation when every scope field it sets equals
the investigation's value. A lesson with no scope applies to every
investigation; only an admin can write one, in the web UI.

When a responder gives a reason with feedback, Sidekick asks the model of the
`summarize` role to draft a lesson from it. With `requireApproval: true` the
lesson waits in the review queue until an admin approves it; with `false` it
is used at once. Without a `summarize` role, no lesson is drafted from
feedback, and Sidekick logs that once at start-up. Admins can still write
lessons by hand. Each draft is one model call, priced and counted in the daily
spend and the daily caps like any other model call; at a cap the draft waits
for the next day.

A prompt shows at most `maxPerPrompt` lessons and at most `maxTokens` tokens
of them. Lessons with more matching scope fields come first, and global
lessons come after all scoped ones. Global lessons may use at most
`globalShare` of the places and of the tokens, so they cannot crowd out
specific ones. Lessons that did not fit are listed in the investigation's
record, so you can see when a scope is too broad.

### `bench`

```yaml
bench:
  gate:
    minRepeats: 5          # at least 5; default 5
    maxDrop: 0.02          # 0 to under 1; default 0.02
    maxCostUSD: 5          # above 0; default 5
    publicDir: ""          # default none
    baseDir: ""            # default the working directory
```

These settings are for the benchmark gate that checks
[proposed changes to strategies](#proposed-changes). They are checked
whether or not proposals are on.

- `minRepeats` is how many times the gate runs each scenario with each
  version. It cannot be set below 5, because fewer runs cannot tell a change
  from chance.
- `maxDrop` is how far below the current version the lower end of the 95%
  interval of the change's score difference may be and still pass.
- `maxCostUSD` caps what one gate may spend on runs and grading. The gate
  stops before a run that would pass it, and ends with the result
  `over_budget`.
- `publicDir` is a directory with the public benchmark scenarios (see
  [the public scenarios](benchmark.md#the-public-scenarios)) that the gate
  chooses from, together with the reviewed scenarios of the database
  (exported from confirmed investigations, see
  [Exporting a real investigation](benchmark.md#exporting-a-real-investigation)).
  Either source is enough; Sidekick logs a note at start-up when proposals are
  on and `publicDir` is empty, since only reviewed scenarios can then be
  chosen. Sidekick does not
  ship them in its image: mount a checkout of the repository, or just its
  `bench` directory, and set `publicDir` to `<checkout>/bench/scenarios/public`
  and `baseDir` to `<checkout>`, since the tool snapshots the scenarios name
  are relative to it, also those of the reviewed scenarios. Empty means the
  gate has only the reviewed scenarios, and a gate with none that covers the
  strategy ends with `no_benchmark_coverage`.

The gate runs its investigations in a schema of its own in the database of
`database.urlSecret`, and drops it at the end, so they never mix with real
ones. The database role must therefore be allowed to create a schema: give it
`CREATE` on the database when proposals are on. Without it a gate ends with
the result `error` and says so.

### `redaction`

Redaction removes secrets and personal data from event text, raw webhook
payloads and tool results before Sidekick stores them or sends them to a
model.

```yaml
redaction:
  builtIn: [cloud_keys, vcs_tokens, slack_tokens, jwt, private_keys, emails]
  custom:
    - name: customer_id
      pattern: "cus_[A-Za-z0-9]{14}"   # a regular expression
  hashKeySecret: { name: sidekick-redaction, key: key }
```

`builtIn` lists the built-in rules to use. Leave it out to use every rule
that is on by default; set it to `[]` to turn every built-in rule off.

| Rule | Finds | On by default |
|---|---|---|
| `cloud_keys` | AWS access key IDs and secret access keys, Google API keys, and the key ID in a GCP service account key file | yes |
| `vcs_tokens` | GitHub and GitLab tokens | yes |
| `slack_tokens` | Slack tokens and incoming webhook URLs | yes |
| `jwt` | JSON Web Tokens | yes |
| `private_keys` | PEM private key blocks, including the one in a GCP service account key file, and blocks whose end was cut off | yes |
| `bearer_tokens` | the credential in an `Authorization` header (`Bearer`, `Basic` or `Token`), and `Bearer <token>` elsewhere | yes |
| `password_pairs` | the value in pairs such as `password=...` or `client_secret: ...`, where the key ends in `password`, `secret`, `token`, `api_key`, `access_key`, `private_key` or `credentials`, and the password in URLs such as `postgres://user:password@host` | yes |
| `emails` | email addresses | yes |
| `card_numbers` | card numbers of 13 to 19 digits, with or without spaces or hyphens, that pass the Luhn check | yes |
| `ip_addresses` | IPv4 and IPv6 addresses | no, because they are often useful evidence |

`custom` adds rules of your own. Each has a `name` and a `pattern`, a
regular expression in [Go's syntax](https://pkg.go.dev/regexp/syntax), and
the whole match is redacted. A name starts with a lower-case letter, holds
only lower-case letters, digits, `_` and `-`, is at most 32 characters long,
and is not the name of a built-in rule.

Each match is replaced with `[REDACTED:<rule>:<hash>]`, for example
`[REDACTED:emails:3f9a1c]`. The hash is the first 6 hex characters of an
HMAC-SHA256 of the value, keyed with the secret that `hashKeySecret` points
to, so the same value always gets the same placeholder and a model can still
tell that two log lines mention the same address. Use a long random key, for
example from `openssl rand -base64 32`; the file's bytes are used exactly
as they are, and an empty file stops Sidekick from starting. Without `hashKeySecret`, Sidekick
makes a random key at start-up and logs a warning, and placeholders change
after every restart.

In a JSON payload, every string is redacted, object keys included, and the
result is still valid JSON. A string value under a key that names a secret,
such as `password` or `Authorization`, is redacted as a whole. Text and tool
results longer than 256 KiB are cut and marked as truncated.

The IDs that locate a Slack or Teams message (the team, tenant, channel,
message and thread IDs or timestamps, the sender's user ID and the Teams
service URL) are stored as received, even when a rule matches them, because
replies are sent to them. The message text is redacted like any other.

### `telemetry`

```yaml
telemetry:
  otlpEndpoint: http://otel-collector.observability:4317   # empty turns trace export off
  logLevel: info                                            # debug, info, warn or error
```

### `watch`

Watchers are periodic, read-only checks that tell when a service is back
up after an alert. This section holds their global limits, defaults and
bounds, and the rules for their HTTP checks. Only this file sets them;
neither the model nor runtime rules can change them.

```yaml
watch:
  allowModel: false                  # offer the start_watcher tool; default false
  maxPerGroup: 3                     # watchers one group may have
  maxTotal: 100                      # watchers running at once in the deployment
  defaults:                          # used when nothing more specific is set
    interval: 30s                    # time between checks
    timeout: 60m                     # how long a watcher may run without recovering
    passesToRecover: 2               # passing checks in a row that count as recovered
    checkTimeout: 10s                # time limit of one check
  minInterval: 10s                   # global bounds
  maxInterval: 15m
  maxTimeout: 4h
  http:
    allowHosts: ["*.internal.example.com", "status.example.com"]
    allowPrivate: ["10.20.0.0/16"]   # private ranges are blocked unless listed
```

A check is one of three types:

- `http` passes when a `GET` of `url` returns one of `expect.status`
  (default `[200]`), and the body matches `expect.bodyRegex` when it is set.
  `headers` take their values only from secrets, as
  `{ name: <secret>, key: <key> }`, and `timeout` overrides the check time
  limit.
- `tool` calls a source tool with the fixed `args` and passes when the
  result meets `condition`: either `jsonPath`, `op` and `value` on the
  structured result, or `textRegex` (with `negate: true` to pass when it
  does not match) on its text. `noData: pass` passes a result without data;
  the default is `fail`.
- `alert` passes when the sender's recovery events have arrived for the
  group's alerts: all of them with `require: all` (the default), or one with
  `require: any`. One passing check recovers it.

A watcher ends in one of these states:

- `recovered`: `passesToRecover` checks in a row passed. A failed check, or
  one that could not run, starts the count again.
- `timed_out`: it ran for `timeout` without recovering.
- `stopped`: every alert of the group recovered (`group_resolved`; an `alert`
  watcher counts as recovered instead), a responder stopped it with the
  Stop watching button in Slack (`responder`), or a config change no longer
  allows its check, for example a host taken off `allowHosts` or a source
  removed (`config_changed`).

Each change of a watcher's state, from its start to its end, is shown in
the investigation's outputs, such as the
[Slack message](slack.md#what-it-posts) and
[webhook callbacks](webhook-callbacks.md#watcher-events), and goes to the
investigation's inbox, so a running investigation sees it before the
model's next answer. The model weighs it as evidence; a watcher never
decides the verdict by itself.

A group whose grouping window ends while one of its watchers runs stays
open, so events with the same fingerprint still join it, and closes when
its last watcher ends. When Sidekick itself was down, a watcher runs its
next check once it is back; checks it missed are not made up.

With `allowModel: true`, investigations can start watchers with the
`start_watcher` tool, within each route's `watcherLimits` (see
[Watchers](#watchers)). A request outside the limits or the bounds, or over
`maxPerGroup` or `maxTotal`, gets an error that names the limit, and
nothing starts. A route or strategy watcher over `maxPerGroup` or
`maxTotal` is not started: a line is logged, and the investigation's inbox
says which watcher did not start and why.

`allowHosts` lists the hosts an HTTP check may reach: exact host names or
IP addresses, or `*.` and a name, which matches every name below it
(`*.internal.example.com` matches `api.internal.example.com` but not
`internal.example.com` itself). Names match without regard to case. With no
`allowHosts`, HTTP checks cannot reach anything.

Even on an allowed host, an HTTP check cannot reach a private or special
address unless `allowPrivate` lists it, as a range in CIDR form or as one
address. The blocked ranges are:

| IPv4 | IPv6 |
|---|---|
| `0.0.0.0/8`, `127.0.0.0/8` (loopback) | `::`, `::1` (loopback), `::/96` |
| `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (private) | `fc00::/7` (unique local) |
| `100.64.0.0/10` (carrier-grade NAT) | `fe80::/10` (link-local), `fec0::/10` |
| `169.254.0.0/16` (link-local, including the cloud metadata address `169.254.169.254`) | `ff00::/8` (multicast) |
| `224.0.0.0/4` (multicast), `240.0.0.0/4` (reserved and broadcast) | `64:ff9b:1::/48`, `100::/64`, `2001::/23`, `2001:db8::/32`, `2002::/16` |
| `192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24` | |

An IPv4-mapped address (`::ffff:10.0.0.1`) or a NAT64 address
(`64:ff9b::a00:1`) counts as the IPv4 address it carries.

HTTP checks also follow these rules, which cannot be turned off:

- Only `GET`, only `http` and `https`, with TLS certificates verified. No
  cookies are kept or sent, and proxy settings from the environment are not
  used.
- The host is resolved before the request and every address it resolves
  to is checked. The address the connection actually goes to is checked
  again just before connecting, so a DNS answer that changes in between
  cannot reach a blocked address.
- Redirects are followed only to the same host, and at most 3. Any other
  redirect is not followed; the redirect itself is the response.
- At most 64 KiB of a response body is read. Bodies are redacted before any
  part of them is stored.
- At most 2 checks run at once against one host, in each replica.

A refused request ends the check with an error and is counted in
`sidekick_watch_http_blocked_total`, by reason: `host`, `private_ip` or
`redirect`.

### `retention`

How long Sidekick keeps each kind of record. Once a day, old records are
deleted. Only this file sets these periods. Each is a whole number of days
such as `90d`, or a duration such as `36h`.

```yaml
retention:
  events: 90d          # received alerts and messages, by when they arrived
  investigations: 365d # investigations, with their report, by when they were created
  steps: 90d           # the full record of each step of a run, by when it started
  auditLog: 730d       # audit log entries, by when they were written
  watcherChecks: 30d   # results of single watcher checks, by when they ran
  memory: 730d         # memory entries of confirmed investigations, by when they were confirmed
```

`steps` and `watcherChecks` cannot be longer than `investigations`,
because deleting an investigation deletes its steps and its watchers with
their checks. `memory` can be longer: a [memory](#memory) entry keeps its
own copy of what it shows, so it stays after its investigation is
deleted.

Every replica checks once an hour, and at start-up, that the retention
job of the day (in UTC) is queued; however many replicas there are, it
runs once a day. It deletes at most 5,000 rows at a time, oldest first,
each batch in a short transaction of its own, so it does not hold locks
for long. It also deletes finished background jobs: done jobs after 1
day, and dead jobs after 30 days. `sidekick_retention_deleted_rows_total`
counts what it deleted, by table.

Deleting an investigation also deletes its steps, its inbox, its
published message references, its feedback, its record of the
[memory](#memory) examples its prompts showed, its watchers with their
checks, and its event group. An investigation whose event group is
still open is kept until the group closes.

With the defaults, an investigation outlives its events and its steps.
An investigation older than `events` and `steps` keeps its verdict,
report, costs and feedback, but no longer has the alert or message that
started it, nor the full record of its tool calls and model turns.
Responders can still rate it, and its message is still updated; it then
names its subject "the alert", since the alert's title is gone. A
follow-up question on an investigation that lost any of its steps is not
investigated: it gets a short answer that the investigation is past its
retention period and its details were deleted.

The audit log can be deleted only by the retention job: the database
rejects any other `DELETE` or `TRUNCATE` of it, as it rejects every
`UPDATE`. This guards against bugs and stray queries, not against someone
with access to the database, who can turn the guard off.

## Defaults

| Setting | Default |
|---|---|
| `server.listen` | `:8080` |
| `server.metricsListen` | `:9090` |
| `database.maxConns` | `20` |
| `database.migrations` | `startup` |
| `secrets.dir` | `/var/run/secrets/sidekick` |
| `intake.webhooks.<name>.auth.tolerance` | `5m` |
| `slack.mode` | `socket` |
| `slack.followUps` | `mention` |
| `slack.apiURL` | `https://slack.com/api/` |
| `oidc.groupsClaim` | `groups` |
| `routes[].priority` | `100` |
| `routes[].strategy` | `generic-alert` |
| `routes[].mode` | `read-only` |
| `actions.proposalTTL` | `24h` |
| `actions.maxPerInvestigation` | `5` |
| `validator.mode` | `sidecar` |
| `validator.url` | `http://127.0.0.1:8200` |
| `validator.job.*` | none: `validator.job.image` is required in job mode |
| `routes[].budgets` | the values in `budgets.perInvestigation` |
| `routes[].budgets.daily.maxCostUSD` | none: only `budgets.daily.maxCostUSD` applies |
| `routes[].budgets.daily.onDailyCap` | `budgets.daily.onDailyCap` |
| `budgets.perInvestigation.maxTokens` | `400000` |
| `budgets.perInvestigation.maxToolCalls` | `40` |
| `budgets.perInvestigation.maxWallTime` | `10m` |
| `budgets.perInvestigation.maxCostUSD` | `1.00` |
| `budgets.daily.maxCostUSD` | none |
| `budgets.daily.timeZone` | `UTC` |
| `budgets.daily.onDailyCap` | `wait` |
| `budgets.concurrency` | `8` |
| `routes[].concurrency` | none: only `budgets.concurrency` applies |
| `budgets.rateLimit`, `routes[].rateLimit` | none |
| `rateLimit.overLimit` | `delay` |
| `routes[].grouping.window` | `30m` |
| `routes[].grouping.fingerprintLabels` | none: the intake's own fingerprint |
| `report.minFalsePositiveConfidence` | `high` |
| `memory.enabled` | `true` |
| `memory.examples` | `3` |
| `memory.vector.enabled` | `false` |
| `lessons.requireApproval` | `true` |
| `lessons.maxPerPrompt` | `10` |
| `lessons.maxTokens` | `1500` |
| `lessons.globalShare` | `0.3` |
| `strategies.proposals.enabled` | `false` |
| `strategies.proposals.wrongThreshold` | `3` |
| `strategies.proposals.window` | `14d` |
| `bench.gate.minRepeats` | `5` |
| `bench.gate.maxDrop` | `0.02` |
| `bench.gate.maxCostUSD` | `5` |
| `bench.gate.publicDir`, `bench.gate.baseDir` | none |
| `redaction.builtIn` | every built-in rule except `ip_addresses` |
| `telemetry.logLevel` | `info` |
| `routes[].watcherLimits.types` | every check type: `http`, `tool` and `alert` |
| `routes[].watcherLimits.tools` | none: the model cannot start tool checks |
| `routes[].watcherLimits.hosts` | the hosts in `watch.http.allowHosts` |
| `watch.allowModel` | `false` |
| `watch.maxPerGroup` | `3` |
| `watch.maxTotal` | `100` |
| `watch.defaults` | `interval: 30s`, `timeout: 60m`, `passesToRecover: 2`, `checkTimeout: 10s` |
| `watch.minInterval`, `watch.maxInterval`, `watch.maxTimeout` | `10s`, `15m`, `4h` |
| `watch.http.allowHosts` | none: HTTP checks cannot reach any host |
| `watch.http.allowPrivate` | none: every private and special range is blocked |
| `retention.events`, `retention.steps` | `90d` |
| `retention.investigations` | `365d` |
| `retention.auditLog` | `730d` |
| `retention.watcherChecks` | `30d` |
| `retention.memory` | `730d` |

## What is checked

Besides unknown keys and values of the wrong type, Sidekick checks that:

- Connector, source, output, webhook endpoint, listener and route names
  are unique, start with a lower-case letter, hold only lower-case
  letters, digits and `-`, and are at most 24 characters long. No webhook
  endpoint and listener share a name, and neither is named
  `slack-mention` or `teams-mention`, which mentions use.
- Every `provider`, source `type`, `adapter` and output `type` is one this
  version of Sidekick has, and its section passes that plugin's own checks.
- `model.roles.investigate` is set, every role names a defined connector,
  and the connectors for `investigate`, `triage` and `judge`, globally or on a
  route, can call tools.
- Every secret reference has a `name` and a `key`, each a plain name without
  `/`. At start-up, every referenced secret file must exist and be readable.
- Every webhook endpoint sets exactly one of `bearer` and `hmac`. Every
  `generic` endpoint sets `fields.title`, and each of its field paths
  parses.
- There is exactly one `default` route, and it has no `match`. Every source,
  output, intake and strategy a route names is defined. Glob and regular
  expression matches parse.
- `slack.mode` is `socket` or `events`, and `events` has `signingSecret`.
  `trustedBots` are bot IDs, `roleGroups` keys are user group IDs,
  `roleUsers` keys are user IDs, every role is a known role, and listeners
  are set only when the Slack app receives events. Each listener has a
  channel name or ID, a `sender.kind` of `human` or `bot`, bot and user
  IDs of the right shape, regular
  expressions that parse, `extract` patterns with at least one named
  group, and `trusted: true` only when `slack.trustedBots` is set.
- `teams` has an `appID` and a `tenantID` of the right shape and exactly
  one of `clientSecret` and `workloadIdentity: true`; `trustedBots` are
  `28:` bot IDs, and `serviceURL` is https and ends in `/`. A listener's
  `source` is `slack` or `teams`; listeners with `source: teams` need the
  `teams` section, a Teams channel ID, Teams bot IDs and Microsoft Entra
  object IDs, and `trusted: true` only with `teams.trustedBots`.
- `oidc` has an `issuer` that is https (or http on a loopback address), a
  `clientID`, a `clientSecret`, a non-empty `groupsClaim` and `scopes`,
  and at least one known role in `roles`, each with at least one group.
  `"*"` is used only for `viewer`, and never with
  `https://accounts.google.com`.
  With `oidc` set, `server.publicURL` is required and must be https (or
  http on a loopback address).
- Every strategy file in `strategies.dir` is valid, and no two of them have
  the same name.
- Every `actions` entry is a registered type with a valid section, and the
  two settings are positive. Every route is `read-only` with no `actions`,
  or `approved` or `read-write` with at least one, listed once and defined
  in `actions`, a `read-write` route can select only trusted intakes and
  listeners unless it sets `allowUntrustedWrite: true`, and its budgets, daily
  cap and `concurrency` are at or below the global ones, and its
  `rateLimit` is no looser than `budgets.rateLimit`. The global
  per-investigation limits and `budgets.concurrency` are positive, dollar
  amounts are not negative, `budgets.daily.timeZone` is a known time zone,
  `onDailyCap` is `wait` or `skip`, and `overLimit` is `delay` or `skip`.
  Each route's `grouping.window` is positive, and no
  `grouping.fingerprintLabels` entry is empty or listed twice.
- With `memory.vector.enabled`, `memory.vector.embeddings` names a
  provider that can embed (`openai`, `bedrock` or `voyage`) with an
  embedding model it knows, or an OpenAI-compatible server's model with its
  `dimensions`, and `dimensions` is a length the model returns and at most
  2000. No `model.connectors` entry sets `dimensions` or names an embedding
  model.
- `strategies.proposals.wrongThreshold` is at least 1 and `window` is positive. `bench.gate.minRepeats` is at least 5, `maxDrop` is at least 0 and below 1, and `maxCostUSD` is above 0.
- `lessons.maxPerPrompt` is 1 to 50, `lessons.maxTokens` is 200 to 20000, and
  `lessons.globalShare` is 0 to 1.
- Every `redaction.builtIn` entry is a built-in rule. Every custom
  redaction rule has a unique name of the right shape that is not a
  built-in rule's name, and a pattern that parses.
- Every `watch.http.allowHosts` entry is a host name, an IP address, or
  `*.` and a host name, and every `watch.http.allowPrivate` entry is a range
  in CIDR form or an IP address.
- `watch.maxPerGroup`, `watch.maxTotal` and the global bounds are
  positive, no `watch.defaults` value is negative, and the defaults are
  inside the global bounds. Each route's `watcherLimits` bounds are inside the global
  ones, its `types` are known check types, its `tools` are qualified tool
  names of its sources, and its `hosts` are inside `watch.http.allowHosts`.
- Every default and watcher value, after the precedence above, is inside
  the route's and the global bounds. The error names the field and the
  bound, for example
  `routes[0].watchers[0].interval: 5s is below routes[0].watcherLimits.minInterval 15s`.
  When the value is a built-in default that the file does not set, the
  error says so and names the fields that can override it, for example
  `the built-in default interval 30s is below routes[0].watcherLimits.minInterval 45s; set routes[0].watcherDefaults.interval or watch.defaults.interval`.
- Watcher names are unique within a route, each check passes its own
  checks, an `http` check's host is in `watch.http.allowHosts`, and a `tool`
  check's source is one of the route's.
- Every `retention` period is positive, and `retention.steps` and
  `retention.watcherChecks` are at most `retention.investigations`.
- `database.migrations`, `report.minFalsePositiveConfidence` and
  `telemetry.logLevel` are known values, URLs are absolute, and listen
  addresses have a port.

## Checking a file with `sidekick config validate`

```sh
sidekick config validate -f sidekick.yaml
```

It prints `sidekick.yaml is valid` and exits with status 0, or prints every
problem on its own line and exits with status 1:

```text
model.roles.investigate: connector "claud" is not defined (did you mean "claude"?)
routes[0].outputs[1]: output "slak-thread" is not defined (did you mean "slack-thread"?)
```

By default it reads no secret file and builds no connector, source or
output, so it needs no mounted secrets and makes no network calls. That
makes it safe to run in CI or in a GitOps pipeline, for example as a step in
GitHub Actions:

```yaml
- name: Check the Sidekick config
  run: sidekick config validate -f deploy/sidekick.yaml
```

Add `--check-secrets` to run everything start-up runs: it also checks that
every referenced secret file exists and is readable under `secrets.dir`, and
builds every connector, source, adapter and output. Run it where the secrets
are mounted, for example in the Sidekick pod:

```sh
sidekick config validate -f /etc/sidekick/sidekick.yaml --check-secrets
```

Secret values are never printed, only their references.
