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
