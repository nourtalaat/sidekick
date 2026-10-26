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
- [Running Sidekick](docs/running.md): `sidekick serve`, start-up and shutdown, health and readiness probes, metrics, logs and traces.

## License

Apache License 2.0. See [LICENSE](LICENSE).
