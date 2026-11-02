<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo/sidekick-lockup-dark.svg">
    <img src="assets/logo/sidekick-lockup-light.svg" alt="Sidekick" width="360">
  </picture>
</p>

<p align="center">The 3 a.m. page, already looked into.</p>

Sidekick receives alerts from webhooks or Slack, investigates them with the LLM of your choice and your observability, cloud, code and ticketing tools, and tells you whether each alert is a real issue or a false positive. For real issues it finds the likely cause and proposes fixes; for false positives it proposes how to stop the alert from firing again.

## What it does

- Takes alerts from Alertmanager, Grafana, Datadog, Sentry, SigNoz or any sender of JSON, and from Slack channels.
- Investigates each alert with read-only tools for metrics, logs, traces, Kubernetes, cloud accounts, code, tickets and databases.
- Shows the verdict, the evidence and the likely cause in a Slack thread, on a ticket or in a signed webhook callback.
- When a route allows it, proposes changes such as a pull request or a ticket, and by default makes them only after a person approves.
- Runs in your own Kubernetes cluster, with a model from Anthropic, Amazon Bedrock, OpenAI, Azure or Google, or one you host yourself.

> **Status:** early. The latest release is v0.1.0; main has much more since then, and these docs describe main.

## Quick start

[Installing Sidekick](docs/install.md#quick-start) takes you from an empty cluster to a first investigation. To try it without paying for a model, see [Run it for free](docs/free-setups.md).

## Documentation

- [Integrations](docs/integrations.md): every alert sender, source, output, model provider and action, with its guide.
- [Running and operating](docs/operating.md): install, configuration, running, the web UI and API, the validator, upgrades.
- [Quality and cost](docs/quality-and-cost.md): how investigations run, the benchmark, free setups.
- [Extending Sidekick](docs/plugins/README.md): write your own source, adapter, model connector, output or action.
- [Configuration](docs/configuration.md): every section of `sidekick.yaml` and `sidekick config validate`.
- [Sending alerts to Sidekick](docs/webhooks.md): webhook endpoints, authentication and test alerts.
- [How investigations run](docs/investigations.md): the agent loop, the report, limits and restarts.

## License

Apache License 2.0. See [LICENSE](LICENSE).
