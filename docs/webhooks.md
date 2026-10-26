# Sending alerts to Sidekick

Senders deliver alerts to Sidekick over HTTP. Each webhook endpoint you
configure under [`intake.webhooks`](configuration.md#intakewebhooks)
receives deliveries at `POST /hooks/<name>` on the main listener
(`server.listen`), reads them with its adapter, and stores one event per
alert.

## What happens to a delivery

1. Sidekick reads the body, up to 1 MiB. A larger body gets `413`.
2. It authenticates the delivery with the endpoint's bearer token or HMAC
   signature, before anything reads the payload. A delivery that fails gets
   `401`.
3. The adapter reads the payload. A payload it cannot read gets `400`,
   with the reason.
4. Sidekick redacts the event text, the labels and the raw payload with the
   [redaction rules](configuration.md#redaction), and stores each event.
   Events from an endpoint with `trusted: true` are marked trusted; the
   payload cannot change that.
5. It answers `202`.

Each event has a key that the payload decides, so a delivery the sender
sends again stores nothing new: the answer is still `202`, with
`"duplicate": true`.

```json
{"duplicate": false, "events": [{"id": "0192d3a4-...", "status": "received", "duplicate": false}]}
```

`status` is `received` for an alert, and `skipped` for a resolved alert:
for now, resolved alerts are stored but not investigated. A path under
`/hooks/` that names no configured endpoint gets `404`.

Right after the answer, Sidekick sends each received event to the first
[route](configuration.md#routes) that matches it and opens an
investigation for it. For now every event gets an investigation of its
own, even when the same alert fires again while an earlier one is still
open.

Every secret is read again on each delivery, so a rotated token or key is
used without a restart. An endpoint whose secret file is empty refuses
every delivery.

## Authentication

Every endpoint sets exactly one of `bearer` and `hmac` under `auth`.

### Bearer token

The sender sends the token from the secret in the `Authorization` header:

```http
POST /hooks/alertmanager HTTP/1.1
Authorization: Bearer <token>
```

### HMAC signature

The sender signs each delivery with the key from the secret:

- `X-Sidekick-Timestamp` is the time of sending in Unix seconds.
- `X-Sidekick-Signature` is `sha256=` followed by the hex HMAC-SHA256,
  keyed with the secret, of the timestamp, a `.`, and the body.

The timestamp must be within `auth.tolerance` (default `5m`) of the time
the delivery arrives, so a captured delivery cannot be sent again later.
With `curl`:

```sh
body='{"alert": {"id": "build-4711", "title": "Nightly build failed"}}'
ts=$(date +%s)
sig=$(printf '%s.%s' "$ts" "$body" | openssl dgst -sha256 -hmac "$KEY" | sed 's/^.* //')
curl -X POST https://sidekick.example.com/hooks/generic-ci \
  -H "X-Sidekick-Timestamp: $ts" \
  -H "X-Sidekick-Signature: sha256=$sig" \
  -d "$body"
```

A trailing newline in the secret file is ignored, for both the token and
the key.

## Adapters

### `alertmanager`

Reads the webhook payload of Prometheus Alertmanager (version 4) and
stores one event per alert in the delivery. The event's title is the
alert's `summary` annotation, or its `alertname`; the text is its
`description` annotation; the service and severity come from its `service`
and `severity` labels. Alerts with status `resolved` become resolved
events. The `generatorURL` and a `runbook_url` annotation become links.
The adapter has no settings of its own.

Point an Alertmanager receiver at the endpoint with the bearer token:

```yaml
# alertmanager.yml
receivers:
  - name: sidekick
    webhook_configs:
      - url: https://sidekick.example.com/hooks/alertmanager
        send_resolved: true
        http_config:
          authorization:
            credentials_file: /etc/alertmanager/secrets/sidekick-token
```

Alertmanager sends a notification again on every `repeat_interval` while
an alert keeps firing. These repeats have the same key, so they are
duplicates and start no new investigation.

### `generic`

Reads any JSON object and stores one event, with the fields you map with
JSON paths under `fields`:

| Key | Required | Meaning |
|---|---|---|
| `fields.title` | yes | A short summary of the alert. |
| `fields.text` | no | The longer description. |
| `fields.service` | no | The affected service. |
| `fields.severity` | no | The sender's severity name, such as `critical` or `warning`. |
| `fields.key` | no | What identifies the alert, so a repeated delivery is stored once. Without it, the key is the SHA-256 of the whole body, so only byte-for-byte repeats are duplicates. |
| `fields.labels` | no | An object whose members become the event's labels. |

```yaml
intake:
  webhooks:
    generic-ci:
      adapter: generic
      auth: { hmac: { name: sidekick-hooks, key: ci } }
      fields:
        title: $.alert.title
        text: $.alert.description
        service: $.labels.service
        severity: $.alert.level
        key: $.alert.id
        labels: $.labels
```

A path starts with `$` and goes on with `.field`, `["field"]` for names
with other characters (`$.labels["app.kubernetes.io/name"]`), and `[n]` for
the element at index `n` of an array, counting from 0. `[*]` selects every
element of an array, but each field must find one value, so it is only
useful inside a longer path that ends up at one.

Strings, numbers and `true` or `false` are taken as text. A delivery is
rejected with `400` when the title or a configured key finds nothing, when
a path finds more than one value, when a field finds an object or an
array, and when the labels are not an object of such values. Other paths
that find nothing leave their part of the event empty.

### Monitoring tools

These adapters read the webhooks of one monitoring tool each. They have
no field mapping to set up; [Setting up senders](senders.md) shows how to
point each tool at Sidekick and which fields become what.

| Adapter | Sender |
|---|---|
| `signoz` | A SigNoz webhook notification channel. |
| `datadog` | Datadog's webhooks integration, with the payload the setup page gives. |

### Severities

Every adapter maps the sender's severity name, in any case, to one of four
levels:

| Level | Names |
|---|---|
| `critical` | `critical`, `crit`, `fatal`, `emergency`, `emerg`, `alert`, `page`, `high`, `error`, `err` |
| `warning` | `warning`, `warn`, `medium` |
| `info` | `info`, `informational`, `notice`, `low`, `none` |
| `unknown` | anything else, or no severity |

## Monitoring intake

Two metrics on `/metrics` show what arrives:

| Metric | Labels | Meaning |
|---|---|---|
| `sidekick_events_received_total` | `intake`, `result` | `accepted` and `duplicate` count events; `rejected` counts deliveries turned away before anything was stored. |
| `sidekick_events_rejected_total` | `intake`, `reason` | Rejected deliveries by reason: `unauthenticated`, `too_large` or `invalid_payload`. |

Deliveries to an endpoint that is not configured count under the intake
`_unknown`, with the reason `unauthenticated`. A delivery that fails
because of Sidekick itself, such as a secret file that cannot be read or a
database error, gets `500`, is logged with the error, and is not counted.

Each delivery is traced in an `intake.receive` span with the attributes
`sidekick.intake`, `sidekick.event.key` (the stored keys) and
`sidekick.duplicate`, or `sidekick.rejected` with the reason. Payloads are
never logged and never become span attributes.

## Writing an adapter

To support a sender with a payload of its own, see
[Writing an intake adapter](plugins/intake-adapters.md).
