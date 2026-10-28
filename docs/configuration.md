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

The Helm chart will also offer a `job` mode, which migrates in a Job before
each upgrade and runs Sidekick in `manual` mode.

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
      model: claude-opus-5-5
      maxTokens: 16000
      thinking: { mode: adaptive }
      effort: high
      timeout: 120s
      retries: 3
      apiKeySecret: { name: sidekick-anthropic, key: apiKey }
  roles:
    investigate: claude      # required, and the model must be able to call tools
    summarize: claude
  prices:
    claude-opus-5-5: { inputPerMTok: 4, outputPerMTok: 20, cacheReadPerMTok: 0.2, cacheWritePerMTok: 5 }   # replaces the shipped price
```

Each provider accepts the shared settings above plus its own. A setting the
model does not support, such as an effort level it lacks, is an error.

Sidekick ships a price table for the models it knows, used for the cost of
every step and for the budgets. An entry under `prices` replaces the shipped
price of that model entirely, so set every field. A model with no price
counts as free in the budgets, so add a price for any model the table lacks.

#### The `anthropic` provider

```yaml
claude:
  provider: anthropic
  model: claude-opus-5-5                  # required
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
    toolCalling: true          # needed for the investigate role
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
  think, for example Claude Opus 5.5; for them `mode: off` is an error, and
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
- **Known models** are the OpenAI models of the `openai` provider, named by
  their model ID in `model`, whatever the deployment is called. For any
  other model, add a `capabilities` block like the `openai` one.
- **API.** The Responses API is the default, as Microsoft recommends for
  Azure OpenAI models; set `api: chat_completions` to use Chat Completions.
  Reasoning is kept by Sidekick, as for the `openai` provider, and is not
  shared with OpenAI's own API.
- **Thinking** is not accepted; use `effort`.
- **Errors.** A prompt that Azure's content filter blocks, and a
  deployment that does not exist, fail the step without retries.
- **Prices.** The shipped OpenAI prices apply, by `model`. They are the
  prices of Global Standard deployments; Data Zone and Regional
  deployments cost 10% more, so set them with `prices`.

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
for senders you control.

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
  trustedBots: [B01SIGNOZ]                                  # bot IDs whose messages can be trusted
  apiURL: https://slack.com/api/                            # optional
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
- `trustedBots` lists the bot IDs (`B...`) whose messages are trusted in a
  listener marked `trusted: true`. Every other message is untrusted.
- `apiURL` is the Slack Web API base URL, ending in `/`. GovSlack
  workspaces use `https://slack-gov.com/api/`.

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
`mode: events`) must be set.

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
- `linear.comment` and `jira.comment` post the result as a comment on the
  Linear or Jira issue the alert names. [Results on tickets](tickets.md)
  describes their keys and how they find the issue.
- `webhook` posts the final result and watcher events as JSON to `url`,
  signed with `hmacSecret` the way webhook intake checks deliveries.
  [Webhook callbacks](webhook-callbacks.md) describes the body, the
  headers and how to check them.

### `strategies`

A strategy tells an investigation what to check first, the usual causes and
the signs of a false positive for one kind of event. Each route names one.

```yaml
strategies:
  dir: /etc/sidekick/strategies   # extra strategy files, read-only
```

Sidekick ships with built-in strategies; this release has `generic-alert`,
which fits any alert. `dir` adds your own: every `*.yaml` or `*.yml` file at
the top of the directory is one strategy. Hidden files and other files are
skipped. A `dir` that is not set, or does not exist, adds none; one that
exists but cannot be read stops Sidekick from starting.

- Sidekick only reads these files. To change a strategy, change its file,
  for example through version control and a new deploy, and restart.
- A file strategy with the name of a built-in one replaces it, and start-up
  logs a line saying which one.
- Each strategy's version is a hash of its file content, so every
  investigation records the exact strategy text it used.

A strategy file looks like this. Unknown keys are errors, as in
`sidekick.yaml`.

```yaml
name: latency-alert                # required; lower-case letters, digits and "-"
description: A latency monitor fired for an HTTP or RPC service.   # required
match:                             # the events the strategy fits; used to pick
  alertNames: ["*Latency*"]        # a strategy in a later release
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
    outputs: [slack-thread]
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

For now every route is `mode: read-only`, which is also the default, and
`actions` must be empty. Modes that let Sidekick take actions come in a
later release.

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
with what it found. See [Limits](investigations.md#limits).

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
  watcher counts as recovered instead), or a config change no longer allows
  its check, for example a host taken off `allowHosts` or a source removed
  (`config_changed`).

A group whose grouping window ends while one of its watchers runs stays
open, so events with the same fingerprint still join it, and closes when
its last watcher ends. When Sidekick itself was down, a watcher runs its
next check once it is back; checks it missed are not made up.

With `allowModel: true`, investigations can start watchers with the
`start_watcher` tool, within each route's `watcherLimits` (see
[Watchers](#watchers)). A request outside the limits or the bounds, or over
`maxPerGroup` or `maxTotal`, gets an error that names the limit, and
nothing starts. A route or strategy watcher over `maxPerGroup` or
`maxTotal` is not started, and a line is logged.

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
| `slack.apiURL` | `https://slack.com/api/` |
| `routes[].priority` | `100` |
| `routes[].strategy` | `generic-alert` |
| `routes[].mode` | `read-only` |
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
  and the connector for `investigate`, globally or on a route, can call
  tools.
- Every secret reference has a `name` and a `key`, each a plain name without
  `/`. At start-up, every referenced secret file must exist and be readable.
- Every webhook endpoint sets exactly one of `bearer` and `hmac`. Every
  `generic` endpoint sets `fields.title`, and each of its field paths
  parses.
- There is exactly one `default` route, and it has no `match`. Every source,
  output, intake and strategy a route names is defined. Glob and regular
  expression matches parse.
- `slack.mode` is `socket` or `events`, and `events` has `signingSecret`.
  `trustedBots` are bot IDs, and listeners are set only when the Slack app
  receives events. Each listener has a channel name or ID, a `sender.kind`
  of `human` or `bot`, bot and user IDs of the right shape, regular
  expressions that parse, `extract` patterns with at least one named
  group, and `trusted: true` only when `slack.trustedBots` is set.
- `teams` has an `appID` and a `tenantID` of the right shape and exactly
  one of `clientSecret` and `workloadIdentity: true`; `trustedBots` are
  `28:` bot IDs, and `serviceURL` is https and ends in `/`. A listener's
  `source` is `slack` or `teams`; listeners with `source: teams` need the
  `teams` section, a Teams channel ID, Teams bot IDs and Microsoft Entra
  object IDs, and `trusted: true` only with `teams.trustedBots`.
- Every strategy file in `strategies.dir` is valid, and no two of them have
  the same name.
- Every route is `read-only` with no `actions`, and its budgets, daily
  cap and `concurrency` are at or below the global ones, and its
  `rateLimit` is no looser than `budgets.rateLimit`. The global
  per-investigation limits and `budgets.concurrency` are positive, dollar
  amounts are not negative, `budgets.daily.timeZone` is a known time zone,
  `onDailyCap` is `wait` or `skip`, and `overLimit` is `delay` or `skip`.
  Each route's `grouping.window` is positive, and no
  `grouping.fingerprintLabels` entry is empty or listed twice.
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
