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
```

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
    claude-opus-5-5: { inputPerMTok: 5, outputPerMTok: 25, cacheReadPerMTok: 0.5 }
```

Each provider accepts the shared settings above plus its own. A setting the
model does not support, such as an effort level it lacks, is an error.

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

### `slack`

```yaml
slack:
  botTokenSecret: { name: sidekick-slack, key: botToken }   # required when the section is set
```

### `outputs`

Outputs are where reports go. Each one has a `type`.

```yaml
outputs:
  slack-thread: { type: slack, channel: "#alerts" }
```

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
  - name: default
    strategy: generic-alert
    outputs: [slack-thread]
```

A route without `strategy` uses the built-in `generic-alert` strategy.

For now every route is `mode: read-only`, which is also the default, and
`actions` must be empty. Modes that let Sidekick take actions come in a
later release.

### `budgets`

The global limits for one investigation. They are the defaults for routes
and the ceilings route budgets must stay within.

```yaml
budgets:
  perInvestigation: { maxToolCalls: 40, maxWallTime: 10m }
```

### `report`

```yaml
report:
  minFalsePositiveConfidence: high   # low, medium or high; default high
```

A false positive verdict below this confidence is not accepted. Lower it
only if you accept that a real incident may be reported as a false positive
more often.

### `redaction`

```yaml
redaction:
  builtIn: [cloud_keys, vcs_tokens, slack_tokens, jwt, private_keys, emails]
  custom:
    - name: customer_id
      pattern: "cus_[A-Za-z0-9]{14}"   # a regular expression
  hashKeySecret: { name: sidekick-redaction, key: key }
```

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
| `secrets.dir` | `/var/run/secrets/sidekick` |
| `intake.webhooks.<name>.auth.tolerance` | `5m` |
| `routes[].priority` | `100` |
| `routes[].strategy` | `generic-alert` |
| `routes[].mode` | `read-only` |
| `routes[].budgets` | the values in `budgets.perInvestigation` |
| `budgets.perInvestigation.maxToolCalls` | `40` |
| `budgets.perInvestigation.maxWallTime` | `10m` |
| `report.minFalsePositiveConfidence` | `high` |
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
- Every webhook endpoint sets exactly one of `bearer` and `hmac`.
- There is exactly one `default` route, and it has no `match`. Every source,
  output, webhook and strategy a route names is defined. Glob and regular
  expression matches parse.
- Every strategy file in `strategies.dir` is valid, and no two of them have
  the same name.
- Every route is `read-only` with no `actions`, and its budgets are at or
  below the global ones.
- `report.minFalsePositiveConfidence` and `telemetry.logLevel` are known
  values, URLs are absolute, and listen addresses have a port.

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
