<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo/sidekick-lockup-dark.svg">
    <img src="assets/logo/sidekick-lockup-light.svg" alt="Sidekick" width="360">
  </picture>
</p>

<p align="center">The 3 a.m. page, already looked into.</p>

Sidekick receives alerts from webhooks or Slack, investigates them with the LLM of your choice and your observability, cloud, code and ticketing tools, and tells you whether each alert is a real issue or a false positive. For real issues it finds the likely cause and proposes fixes; for false positives it proposes how to stop the alert from firing again.

> **Status:** early. v0.1.0 is the first release: webhook alerts, investigations with Claude and MCP sources, and the verdict in a Slack thread. [Installing Sidekick](docs/install.md) has the quick start.

## Documentation

- [Installing Sidekick](docs/install.md): the Helm chart, the quick start, verifying the image, production settings, upgrades.
- [Configuration](docs/configuration.md): the `sidekick.yaml` sections, the checks Sidekick runs on them, and `sidekick config validate`.
- [Backstage source](docs/sources/backstage.md): the keys and tools of the Backstage service catalog source, and the token it needs.
- [GitHub source](docs/sources/github.md): the GitHub App permissions the GitHub source needs, and how to set the app up.
- [Google Cloud source](docs/sources/gcp.md): the permissions the Google Cloud source needs, and how to give Sidekick its Google Cloud identity.
- [Kubernetes source](docs/sources/kubernetes.md): the keys and tools of the Kubernetes source, and the read-only RBAC it needs.
- [Postgres source](docs/sources/postgres.md): the read-only role the Postgres source needs, the TLS rules for its URL, and how to turn on `pg_stat_statements`.
- [Running Sidekick](docs/running.md): `sidekick serve`, `sidekick doctor`, start-up and shutdown, health and readiness probes, metrics, logs and traces.
- [Sending alerts to Sidekick](docs/webhooks.md): webhook endpoints, the Alertmanager and generic adapters, how deliveries are authenticated, and `sidekick test-alert`.
- [Setting up senders](docs/senders.md): pointing SigNoz, Datadog, Sentry and Grafana at Sidekick, and what each of their alerts becomes.
- [How investigations run](docs/investigations.md): the agent loop, the report checks, how an investigation ends, its limits, and what happens on a restart.
- [Investigations in Slack](docs/slack.md): how to set up the Slack app from its manifest, how listeners, mentions and the `/sidekick` command start investigations, and what the Slack output posts while an investigation runs and when it finishes.
- [Results on tickets](docs/tickets.md): the Linear and Jira comment outputs, how it finds the ticket an alert is about, and how to set it up.
- [Webhook callbacks](docs/webhook-callbacks.md): the signed JSON callback with each final result and watcher event, and how a receiver checks it.
- [The monitor validator](docs/validator.md): the image that checks monitor files before a pull request, its formats, sidecar and Job modes, and network isolation.
- [Benchmarks](docs/benchmark.md): `sidekick bench run`, scenario files, mock sources and their matchers, and the Postgres a benchmark runs on.

## License

Apache License 2.0. See [LICENSE](LICENSE).
