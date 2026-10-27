# Running Sidekick

This page describes `sidekick serve`: what it does when it starts and stops,
the HTTP endpoints it serves for Kubernetes probes and Prometheus, and its
logs and traces. It also describes `sidekick doctor`, which checks that
Sidekick can reach everything its config names.

## Starting

```sh
sidekick serve --config /etc/sidekick/sidekick.yaml
```

`--config` (or `-c`) defaults to `/etc/sidekick/sidekick.yaml`, where the
Helm chart mounts the file. At start-up Sidekick:

1. Loads the config file and checks it, including that every secret file it
   references exists and is readable. Problems are printed one per line, as
   [`sidekick config validate`](configuration.md#checking-a-file-with-sidekick-config-validate)
   prints them, and Sidekick exits with status 1.
2. Sets up its logs, metrics and traces.
3. Connects to Postgres, with a pool of `database.maxConns` connections.
4. Migrates the database, unless `database.migrations` is `manual` (see
   [the `database` section](configuration.md#database)). When several
   replicas start at once, one migrates and the others wait for it.
5. Builds the configured connectors, sources, webhook adapters and outputs.
6. Starts the job queue, which routes events, runs
   [investigations](investigations.md) and publishes them to the outputs.
7. Starts serving HTTP on both listeners.

`sidekick migrate up` and `sidekick migrate status` take the same
`--config` flag. They read only the database secret, so a migration Job does
not need the other secrets mounted.

## Checking an install with `sidekick doctor`

```sh
kubectl exec deploy/sidekick -c sidekick -- sidekick doctor
```

`sidekick doctor` reads the same config file as `serve` (`--config`,
default `/etc/sidekick/sidekick.yaml`) and checks, in order:

| Check | What it does |
|---|---|
| `database` | Reads `database.urlSecret`, connects, and compares the schema with the migrations this release needs. |
| `slack` | Calls Slack's `auth.test` with the bot token from `slack.botTokenSecret`, once for each Slack API address the `slack` outputs use. Nothing is posted. |
| `model <name>` | Builds each model connector and checks its API key with a call that costs nothing: for `anthropic`, listing one model. |
| `prices` | Warns about each connector whose model has no price, so its calls would show no cost and count nothing toward the cost budgets and daily caps. Add a `model.prices` entry to fix it. |
| `tool choice <name>` | Warns about each connector that fills the `investigate` role, in `model.roles` or in a route, but cannot be forced to call a tool, such as a local model on Ollama. Investigations still work: when the model ends a turn without calling a tool, Sidekick reminds it to call `submit_verdict`. A local model preset gets a forced tool choice on vLLM with `server: vllm`. |
| `server <name>` | Warns about each `openai` connector that runs a local model preset without `server` set. It calls Ollama's `GET /api/version` next to the `baseURL`, without credentials, and when Ollama answers, the fix is the `server: ollama` line to add. |
| `context <name>` | For each `openai` connector that runs a local model preset on Ollama, with `server: ollama` or detected by the `server` check, calls Ollama's `GET /api/ps` and warns when the model runs with less context than the preset's minimum (65,536 tokens), or when the context cannot be told because the model is not loaded. The fix is to set `OLLAMA_CONTEXT_LENGTH` in Ollama's environment. |
| `source <name>` | Builds each source, waits up to 15 seconds for it to connect and list its tools, and lists the tools the model will not see, with the reason. |

It prints one line per check, with a fix below each problem. The `tool choice`, `server` and `context` checks print a line only for the connectors they apply to. For example:

```text
ok    database: connected; the schema has every migration this release needs (7)
FAIL  slack: Slack rejected the bot token: invalid_auth
      fix: put the app's Bot User OAuth Token (xoxb-...), from the Slack app's OAuth & Permissions page, in the Secret sidekick-slack, key botToken, and reinstall the app if it was removed
ok    model claude: the anthropic API accepted the key
ok    prices: every configured model has a price
skip  sources: no sources
```

Every check runs, even after one fails, so one run shows every problem.
Secret values never appear in the output. The exit status is 1 when any
check fails; warnings and skipped checks leave it at 0.

## Stopping

On SIGTERM or SIGINT, Sidekick stops in the reverse order: it stops taking
requests and lets open ones finish, gives running jobs time to finish and
hands back the ones that do not, so another replica picks them up at once,
then closes its database connections and sends the traces it still holds.
All of this takes at most 30 seconds, which fits the default Kubernetes
termination grace period. A second signal exits at once.

## Endpoints

Sidekick serves two listeners:

| Listener | Setting | Default | Serves |
|---|---|---|---|
| Main | `server.listen` | `:8080` | Webhooks at `POST /hooks/<name>` (see [Sending alerts to Sidekick](webhooks.md)), and the API as later releases add it. Every other path answers 404. |
| Metrics | `server.metricsListen` | `:9090` | `/metrics`, `/healthz` and `/readyz`. |

Keep the metrics listener off the public network.

### `/healthz`

The liveness probe. It answers `200 ok` for as long as the process can serve
requests, and checks nothing else, so a database outage never makes
Kubernetes restart Sidekick.

### `/readyz`

The readiness probe. It answers 200 when Sidekick can do its work, and 503
when it cannot, with one line per check:

```text
config: ok
database: ok
schema: ok
```

- `config`: the config file is loaded.
- `database`: Postgres answers.
- `schema`: the database has every migration this release needs. A newer
  schema is fine: during a rolling update, replicas of the previous release
  keep serving on the schema the new release migrated to. In `manual`
  migration mode, the message says to run `sidekick migrate up`:

  ```text
  schema: the database has migrations up to 1 but this release needs 2; database.migrations is manual, so run `sidekick migrate up`
  ```

A message that would span several lines, such as a database error that lists
each address Sidekick tried, is joined into one line with `; `, so each check
always takes exactly one line.

Sources such as MCP servers are never checked, so one that is down does not
take Sidekick out of service; investigations report it instead.

### `/metrics`

Prometheus metrics in the text format. Besides the Go runtime (`go_*`) and
process (`process_*`) metrics, this release has:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `sidekick_build_info` | gauge | `version`, `commit` | Always 1; the labels name the running build. |
| `sidekick_queue_jobs` | gauge | `kind`, `status` | Jobs in the queue that are pending, running or dead. Every replica reports the same numbers, so combine replicas with `max`. |
| `sidekick_queue_wait_seconds` | histogram | `kind` | Time from a job being ready to being claimed. |
| `sidekick_jobs_dead_total` | counter | `kind` | Jobs that failed for the last time on this replica. |
| `sidekick_events_received_total` | counter | `intake`, `result` | Webhook events by result: `accepted` and `duplicate` count events, `rejected` counts deliveries turned away. See [Monitoring intake](webhooks.md#monitoring-intake). |
| `sidekick_events_rejected_total` | counter | `intake`, `reason` | Rejected webhook deliveries by reason: `unauthenticated`, `too_large` or `invalid_payload`. |
| `sidekick_investigations_total` | counter | `route`, `verdict`, `stop_reason` | Investigations that ended, by verdict and [stop reason](investigations.md#how-an-investigation-ends). |
| `sidekick_investigations_running` | gauge | `route` | Investigations running on this replica. Sum it across replicas. |
| `sidekick_investigation_duration_seconds` | histogram | `route` | Time from the start of an investigation to its end. |
| `sidekick_time_to_first_finding_seconds` | histogram | `route` | Time from the start of an investigation to its first recorded finding. |
| `sidekick_budget_exhausted_total` | counter | `route`, `budget` | Investigations that ended at a limit: `tokens`, `tool_calls`, `wall_time`, `cost` or `daily_cap`. See [Limits](investigations.md#limits). |
| `sidekick_daily_spend_usd` | gauge | `route` | What each route has spent today on model calls, in US dollars. Read from the database at each scrape, so every replica reports the same numbers; combine replicas with `max`. |
| `sidekick_events_skipped_total` | counter | `route`, `reason` | Events that are not investigated, by reason: `rate_limit` or `daily_cap`. See [Daily caps](configuration.md#daily-caps) and [Rate limits](configuration.md#rate-limits). |
| `sidekick_model_calls_total` | counter | `connector`, `model`, `role`, `outcome` | Model calls, one per attempt, by outcome: `ok` or the error kind, such as `rate_limited`. |
| `sidekick_model_call_duration_seconds` | histogram | `connector`, `model` | Duration of model calls. |
| `sidekick_model_tokens_total` | counter | `connector`, `model`, `kind` | Tokens used, by kind: `input`, `output`, `cache_read` or `cache_write`. |
| `sidekick_model_cost_usd_total` | counter | `connector`, `model`, `route` | Cost of model calls in US dollars, from the price table. |
| `sidekick_tool_calls_total` | counter | `source`, `tool`, `outcome` | Source tool calls by outcome: `ok`, `no_data`, `error`, `timeout` or `unavailable`. |
| `sidekick_tool_call_duration_seconds` | histogram | `source` | Duration of source tool calls. |
| `sidekick_source_up` | gauge | `source` | 1 when a source is connected and its circuit breaker is closed, 0 otherwise. |
| `sidekick_guard_keys_full_total` | counter | none | Guarded calls that are not source tool calls, such as watcher HTTP checks, refused because the guard already held limits for 1024 targets, all in use. |
| `sidekick_slack_api_errors_total` | counter | `method`, `error` | Failed Slack Web API calls by method, such as `chat.update`, and error: Slack's error code, such as `channel_not_found`, or `ratelimited`, `request_failed`, `http_status`, `bad_response` or `other`. See [Investigations in Slack](slack.md). |

## Logs

Sidekick logs JSON to standard output, one object per line, at the level
`telemetry.logLevel` sets (default `info`):

```json
{"time":"2026-10-26T09:00:00.000Z","level":"INFO","msg":"sidekick started","component":"app","listen":"[::]:8080","metrics_listen":"[::]:9090"}
```

Every line has `time`, `level` and `msg`, and `component` names the part of
Sidekick that wrote it, such as `app` or `queue`. A line written while
Sidekick works on something it traces also has `trace_id` and `span_id`, so
you can go from a log line to its trace and back.

Each routed event logs `event routed` with `event_id`, `route`, `group_id`
and `investigation_id`. Each run of an investigation logs `investigation
run started`, with `from_seq`, the step it starts from, which is above 0
when it resumes, and `investigation finished` with `status`, `verdict` and
`stop_reason`. Event text, tool results and model output are never logged.

## Traces

Set `telemetry.otlpEndpoint` to send traces to an OpenTelemetry collector
over OTLP gRPC, usually on port 4317:

```yaml
telemetry:
  otlpEndpoint: http://otel-collector.observability:4317
```

With `http://` the traces are sent without TLS; with `https://` they use
TLS. Spans carry `service.name` `sidekick` and `service.version`. The
standard `OTEL_RESOURCE_ATTRIBUTES` variable adds or overrides resource
attributes. Without an endpoint, no trace leaves the process, but log lines
still carry trace and span IDs.

A webhook delivery starts a trace with an `intake.receive` span. Routing
each of its events adds a `router.route` span to the same trace, with the
attributes `sidekick.route` and `sidekick.group.id`, so you can follow an
alert from the delivery to its investigation. Each run of an investigation
adds an `investigation.run` span, with the attributes
`sidekick.investigation.id`, `sidekick.route`, `sidekick.strategy` and
`sidekick.run`; inside it, each model call is a `model.generate` span with
the OpenTelemetry GenAI attributes and `sidekick.cost_usd`, and each source
call a `tool.call` span with `sidekick.source`, `sidekick.tool` and
`sidekick.outcome`. Prompts, tool results and model output are never span
attributes. Each publish job, which shows an investigation in one output,
adds an `output.publish` span with the attributes `sidekick.sink` and
`sidekick.phase`.
