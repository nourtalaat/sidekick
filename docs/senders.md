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
