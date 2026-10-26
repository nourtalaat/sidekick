<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo/sidekick-lockup-dark.svg">
    <img src="assets/logo/sidekick-lockup-light.svg" alt="Sidekick" width="360">
  </picture>
</p>

<p align="center">The 3 a.m. page, already looked into.</p>

Sidekick receives alerts from webhooks or Slack, investigates them with the LLM of your choice and your observability, cloud, code and ticketing tools, and tells you whether each alert is a real issue or a false positive. For real issues it finds the likely cause and proposes fixes; for false positives it proposes how to stop the alert from firing again.

> **Status:** early design. There is no release yet. Installation instructions will appear here with the first release.

## Documentation

- [Configuration](docs/configuration.md): the `sidekick.yaml` sections, the checks Sidekick runs on them, and `sidekick config validate`.
- [GitHub source](docs/sources/github.md): the GitHub App permissions the GitHub source needs, and how to set the app up.
- [Running Sidekick](docs/running.md): `sidekick serve`, start-up and shutdown, health and readiness probes, metrics, logs and traces.
- [Sending alerts to Sidekick](docs/webhooks.md): webhook endpoints, the Alertmanager and generic adapters, and how deliveries are authenticated.
- [Setting up senders](docs/senders.md): pointing SigNoz, Datadog, Sentry and Grafana at Sidekick, and what each of their alerts becomes.
- [How investigations run](docs/investigations.md): the agent loop, the report checks, how an investigation ends, its limits, and what happens on a restart.
- [Investigations in Slack](docs/slack.md): the Slack output, what it posts while an investigation runs and when it finishes, and how to set up the Slack app.

## License

Apache License 2.0. See [LICENSE](LICENSE).
