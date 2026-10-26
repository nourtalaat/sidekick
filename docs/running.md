# Running Sidekick

This page describes `sidekick serve`: what it does when it starts and stops,
the HTTP endpoints it serves for Kubernetes probes and Prometheus, and its
logs and traces.

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
6. Starts the job queue.
7. Starts serving HTTP on both listeners.

`sidekick migrate up` and `sidekick migrate status` take the same
`--config` flag. They read only the database secret, so a migration Job does
not need the other secrets mounted.

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
