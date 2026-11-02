# Running and operating Sidekick

This page groups the guides for installing, configuring and running
Sidekick, and for the tools you use once it runs.

## Install

- [Installing Sidekick](install.md): the Helm chart, the quick start and the changes for production.
- [Quick start](install.md#quick-start): from an empty cluster to a first investigation.
- [Production settings](install.md#production): what to change from the quick-start values.
- [Cloud credentials](install.md#cloud-credentials): how to give Sidekick its identity in each cloud.
- [Helm values](install.md#values): the chart's values.
- [Verifying the image](install.md#verifying-the-image): how to check the image signature.
- [Uninstalling](install.md#uninstalling): how to remove Sidekick.

## Upgrades

- [Upgrading](install.md#upgrading): the Helm command, and how pods are replaced one at a time.

## Configuration

- [Configuration](configuration.md): every section of `sidekick.yaml`.
- [Defaults](configuration.md#defaults): the value each key takes when you leave it out.
- [What is checked](configuration.md#what-is-checked): the checks Sidekick runs on the file.
- [Checking a file](configuration.md#checking-a-file-with-sidekick-config-validate): `sidekick config validate`.

## Running

- [Running Sidekick](running.md): `sidekick serve`, start-up and shutdown.
- [Checking an install](running.md#checking-an-install-with-sidekick-doctor): `sidekick doctor`.
- [Health and readiness](running.md#endpoints): the probe endpoints and the metrics port.
- [Metrics](running.md#metrics): the metrics Sidekick exposes.
- [Logs](running.md#logs) and [traces](running.md#traces): what Sidekick writes and how to follow a request.
- [Sending a test alert](webhooks.md#sending-a-test-alert): `sidekick test-alert`.

## Web UI and API

- [Web UI and API](web-ui.md): OIDC login, roles, sessions and the pages of the UI.
- [Roles](web-ui.md#roles): who may read, answer, approve and administer.
- [API tokens](web-ui.md#api-tokens): access for automation, such as a CI job.
- [The dashboard](web-ui.md#the-dashboard): its filters and the numbers it shows.
- [The JSON API](web-ui.md#the-json-api): the operations under `/api/v1`.

## The validator

- [The monitor validator](validator.md): the image that checks monitor files before a pull request, in sidecar or Job mode.
