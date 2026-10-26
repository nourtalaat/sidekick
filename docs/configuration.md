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

### `slack`

```yaml
slack:
  botTokenSecret: { name: sidekick-slack, key: botToken }   # required when the section is set
```

The bot token of the Slack app. Every `slack` output posts with it, so the
section is required when an output has `type: slack`.

### `outputs`

Outputs are where investigations are shown. Each one has a `type`, and
each route lists the outputs its investigations go to in `outputs`.

```yaml
outputs:
  slack-thread: { type: slack, channel: "#alerts" }   # apiURL is optional
```

This release has one type, `slack`, which posts one message per
investigation and edits it as the investigation runs.
[Investigations in Slack](slack.md) describes its keys, what it posts and
how to set up the Slack app.

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
  maxToolCalls: 30                 # of the two limits applies
  maxWallTime: 5m
```

`watchers` and `watcherDefaults` are accepted and checked, but watchers are
not started until a later release.

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
      intake: [alertmanager]           # webhook endpoint names
      labels: { team: payments }
      service: "checkout-*"            # a glob
      severity: [warning, critical]    # unknown, info, warning or critical
      text: "(?i)latency|p99"          # a regular expression
    strategy: latency-alert
    roles: { investigate: claude }     # overrides model.roles for this route
    sources: [signoz]
    outputs: [slack-thread]
    budgets: { maxToolCalls: 30 }      # at or below the global budgets
    concurrency: 3                     # at most 3 of its investigations at once
  - name: default
    strategy: generic-alert
    outputs: [slack-thread]
```

A route without `strategy` uses the built-in `generic-alert` strategy.

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

For now every route is `mode: read-only`, which is also the default, and
`actions` must be empty. Modes that let Sidekick take actions come in a
later release.

### `budgets`

The global limits for one investigation. They are the defaults for routes
and the ceilings route budgets must stay within.

```yaml
budgets:
  perInvestigation: { maxToolCalls: 40, maxWallTime: 10m }
  concurrency: 8
```

`maxToolCalls` counts calls to source tools, and `maxWallTime` counts from
when the investigation first started, including time lost to a restart. An
investigation that reaches either ends as "Not sure yet" with what it
found; see [Limits](investigations.md#limits).

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

### `telemetry`

```yaml
telemetry:
  otlpEndpoint: http://otel-collector.observability:4317   # empty turns trace export off
  logLevel: info                                            # debug, info, warn or error
```

## Defaults

| Setting | Default |
|---|---|
| `server.listen` | `:8080` |
| `server.metricsListen` | `:9090` |
| `database.maxConns` | `20` |
| `database.migrations` | `startup` |
| `secrets.dir` | `/var/run/secrets/sidekick` |
| `intake.webhooks.<name>.auth.tolerance` | `5m` |
| `routes[].priority` | `100` |
| `routes[].strategy` | `generic-alert` |
| `routes[].mode` | `read-only` |
| `routes[].budgets` | the values in `budgets.perInvestigation` |
| `budgets.perInvestigation.maxToolCalls` | `40` |
| `budgets.perInvestigation.maxWallTime` | `10m` |
| `budgets.concurrency` | `8` |
| `routes[].concurrency` | none: only `budgets.concurrency` applies |
| `report.minFalsePositiveConfidence` | `high` |
| `redaction.builtIn` | every built-in rule except `ip_addresses` |
| `telemetry.logLevel` | `info` |

## What is checked

Besides unknown keys and values of the wrong type, Sidekick checks that:

- Connector, source, output, webhook endpoint and route names are unique,
  start with a lower-case letter, hold only lower-case letters, digits and
  `-`, and are at most 24 characters long.
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
  output, webhook and strategy a route names is defined. Glob and regular
  expression matches parse.
- Every strategy file in `strategies.dir` is valid, and no two of them have
  the same name.
- Every route is `read-only` with no `actions`, and its budgets and
  `concurrency` are at or below the global ones. `budgets.concurrency` is
  at least 1.
- Every `redaction.builtIn` entry is a built-in rule. Every custom
  redaction rule has a unique name of the right shape that is not a
  built-in rule's name, and a pattern that parses.
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
