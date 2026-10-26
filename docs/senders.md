# Setting up senders

This page shows how to point each supported monitoring tool at Sidekick.
Each sender gets its own webhook endpoint under
[`intake.webhooks`](configuration.md#intakewebhooks), with the adapter
named after the sender. How a delivery is authenticated, stored and
answered is the same for every endpoint; see
[Sending alerts to Sidekick](webhooks.md).

In the examples, Sidekick serves `https://sidekick.example.com` and keeps
the webhook secrets in a Kubernetes Secret named `sidekick-hooks`.

## SigNoz

SigNoz sends alerts through a **Webhook** notification channel, in the
payload format of Prometheus Alertmanager. The adapter stores one event
per alert, and a resolved event when an alert resolves.

```yaml
intake:
  webhooks:
    signoz:
      adapter: signoz
      auth: { bearer: { name: sidekick-hooks, key: signoz } }
```

In SigNoz, open **Settings**, then **Notification Channels**, and add a
channel:

1. Set the type to **Webhook** and the URL to
   `https://sidekick.example.com/hooks/signoz`.
2. Leave **Username** empty and put the token from the secret in
   **Password**. With no username, SigNoz sends the password as an
   `Authorization: Bearer` token.
3. Keep **Send resolved alerts** on, and press **Test**.
4. Select the channel in the alert rules that should reach Sidekick.

| Event field | Comes from |
|---|---|
| Title | The `summary` annotation, or the alert name. |
| Text | The `description` annotation. |
| Service | The `service.name` label, then `service_name`, then `service`. |
| Severity | The `severity` label. |
| Labels | Every label of the alert. |
| Links | The rule (`generatorURL` or the `ruleSource` label), a `runbook_url` annotation, and the `related_logs` and `related_traces` annotations. |

SigNoz repeats a notification while an alert keeps firing; the repeats
have the same key, so they store nothing new. Alerts are grouped by their
labels, leaving out `severity` and `threshold.name`, so an alert that
moves from the warning to the critical threshold stays one alert.

## Datadog

Datadog sends monitor notifications through its **Webhooks**
integration. The payload is a template you write; the adapter reads the
one below. Each notification becomes one event, and a `Recovered`
notification a resolved event.

```yaml
intake:
  webhooks:
    datadog:
      adapter: datadog
      auth: { bearer: { name: sidekick-hooks, key: datadog } }
```

In Datadog, open **Integrations**, then **Webhooks**:

1. Under **Variables**, add a custom variable named `SIDEKICK_TOKEN`
   holding the token from the secret, with **Hide from view** checked.
2. Add a webhook named `sidekick` with the URL
   `https://sidekick.example.com/hooks/datadog`.
3. Set **Custom Headers** to
   `{"Authorization": "Bearer $SIDEKICK_TOKEN"}`.
4. Set **Payload** to:

   ```json
   {
     "id": "$ID",
     "title": "$EVENT_TITLE",
     "body": "$TEXT_ONLY_MSG",
     "date": "$DATE",
     "org": {"id": "$ORG_ID", "name": "$ORG_NAME"},
     "alert_id": "$ALERT_ID",
     "alert_title": "$ALERT_TITLE",
     "alert_transition": "$ALERT_TRANSITION",
     "alert_type": "$ALERT_TYPE",
     "alert_priority": "$ALERT_PRIORITY",
     "alert_scope": "$ALERT_SCOPE",
     "tags": "$TAGS",
     "link": "$LINK",
     "snapshot": "$SNAPSHOT"
   }
   ```

5. Mention `@webhook-sidekick` in the message of each monitor that should
   reach Sidekick.

| Event field | Comes from |
|---|---|
| Key | `id`, Datadog's ID of the notification. |
| Title | `title`, or `alert_title`. One of them is required. |
| Alert name | The title without the bracketed parts before the monitor's name, such as `[P1]` and `[Triggered on {host:a}]`. |
| Text | `body`. |
| Service | The `service` tag. |
| Severity | `alert_type` (`error`, `warning` or `info`); otherwise `alert_priority`: `P1` and `P2` are critical, `P3` warning, `P4` and `P5` info. |
| Labels | The tags in `tags` and `alert_scope`, as `name:value`; a tag without a value has an empty one. |
| Start or end | `date`, in milliseconds. A resolved event's `date` is when it ended. |
| Links | `link` (the event in Datadog) and `snapshot` (the graph). |

`alert_transition` `Recovered`, or `alert_type` `success`, makes a
resolved event. `id`, `date`, `alert_id` and `org.id` may be strings or
numbers. Members the payload leaves out stay empty, so Datadog's default
payload works too, but without a service, labels or resolved events.

A delivery Datadog sends again has the same `id`, so it stores nothing
new. A renotification, while a monitor stays in alert, has a new `id` and
is a new event. Events of the same monitor (`alert_id`) and scope
(`alert_scope`) are grouped as one alert.

## Sentry

Sentry sends webhooks from an **internal integration**, and signs each
one with the integration's client secret. The adapter checks that
signature instead of a bearer token or Sidekick's own HMAC scheme, with
the secret that `auth.hmac` names.

```yaml
intake:
  webhooks:
    sentry:
      adapter: sentry
      auth: { hmac: { name: sidekick-hooks, key: sentry } }
```

In Sentry, open **Settings**, then **Developer Settings**, and create an
internal integration:

1. Set the **Webhook URL** to `https://sidekick.example.com/hooks/sentry`.
2. Turn on **Alert Rule Action**, so alert rules can notify it.
3. Under **Permissions**, give **Issue & Event** read access, and under
   **Webhooks** check **issue** if issues that are created or resolved
   should reach Sidekick without an alert rule.
4. Save, and put the **Client Secret** in the secret.
5. In the issue alert and metric alert rules that should reach Sidekick,
   add the action that sends a notification through the integration.

The adapter turns these webhooks into events:

| Webhook | Event | Key |
|---|---|---|
| Issue alert (`triggered`) | An alert. | The event ID and the rule's name. |
| Metric alert `critical` or `warning` | An alert. | The incident's ID and the action. |
| Metric alert `resolved` | A resolved event. | The incident's ID and the action. |
| Issue `created` or `unresolved` | An alert. | The issue's ID, the action and when it was last seen. |
| Issue `resolved` | A resolved event. | The issue's ID, the action and when it was last seen. |

Other webhooks, such as an issue that is assigned or archived, or an
installation or comment, store nothing and get `202`.

| Event field | Issue alert | Metric alert | Issue |
|---|---|---|---|
| Alert name | The rule's name. | The alert rule's name. | The short ID, such as `PYTHON-Y`. |
| Title | The event's title. | `description_title`, or the rule's name. | The issue's title. |
| Text | The culprit. | `description_text`. | The culprit. |
| Service | The `service` tag. | The project, when the rule has one. | The project. |
| Severity | The event's level. | `critical` or `warning`. | The issue's level. |
| Labels | The event's tags, `issue_id` and `project_id`. | `alert_rule_id`, `project` and `environment`. | `issue_id`, `project` and `project_id`. |
| Link | The event. | The alert. | The issue. |

An issue alert and the webhooks of its issue are grouped as one alert, by
the issue's ID; a metric alert's incidents are grouped by the alert rule.

Sentry's signature covers the body only, not the time it was sent, so it
cannot stop a captured delivery from being sent again. Such a repeat has
the same key, so it stores nothing new.

## Grafana

Grafana Alerting sends alerts through a **Webhook** contact point. The
adapter reads its default payload (version 1) and stores one event per
alert in the delivery, and a resolved event when an alert resolves.

Grafana can authenticate with a bearer token, which every Grafana version
with Grafana Alerting can send:

```yaml
intake:
  webhooks:
    grafana:
      adapter: grafana
      auth: { bearer: { name: sidekick-hooks, key: grafana } }
```

Or, in versions that have the **HMAC Signature** settings, it can sign
each delivery. Add a `signature` section; the adapter then checks
Grafana's signature, with the secret that `auth.hmac` names, instead of a
bearer token:

```yaml
intake:
  webhooks:
    grafana:
      adapter: grafana
      auth: { hmac: { name: sidekick-hooks, key: grafana } }
      signature:
        timestampHeader: X-Grafana-Alerting-Timestamp
```

| Key | Default | Meaning |
|---|---|---|
| `signature.header` | `X-Grafana-Alerting-Signature` | The header that holds the signature, as set in Grafana. |
| `signature.timestampHeader` | none | The header that holds the time of sending, as set in Grafana. |
| `signature.tolerance` | `5m` | How far that time may be from the time the delivery arrives. |

Set a timestamp header wherever your Grafana version allows it. Without
one, Grafana signs only the body, so the signature cannot stop a captured
delivery from being sent again later; the repeat still has the same key
and stores nothing new, but it is accepted.

In Grafana, open **Alerting**, then **Contact points**, and add one:

1. Set the integration to **Webhook** and the URL to
   `https://sidekick.example.com/hooks/grafana`.
2. Under **Optional Webhook settings**, either put the token from the
   secret in the credentials of the **Authorization Header** (the scheme
   stays `Bearer`), or fill in **HMAC Signature** with the secret and the
   same header names as the `signature` section.
3. Leave **Custom Payload** empty: the adapter reads only the default
   payload. **Title** and **Message** are ignored.
4. Leave **Disable resolved message** off, and press **Test**.
5. Point a notification policy at the contact point.

| Event field | Comes from |
|---|---|
| Title | The `summary` annotation, or the alert name. |
| Text | The `description` annotation. |
| Service | The `service` label. |
| Severity | The `severity` label. |
| Labels | Every label of the alert. |
| Links | The alert rule (`generatorURL`), a `runbook_url` annotation, and the dashboard, panel and image of the alert, when it has them. |

Grafana repeats a notification while an alert keeps firing; the repeats
have the same key, so they store nothing new.
