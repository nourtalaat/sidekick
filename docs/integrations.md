# Integrations

This page lists what Sidekick connects to, grouped by what each part does,
with a link to the guide for each one. The names in code format are the
`adapter`, `type` or `provider` values you write in `sidekick.yaml`. A
type with no guide of its own links to its section of the
[configuration](configuration.md).

## Alert senders and intake

Webhook adapters read the alerts that a sender posts to an endpoint of the
[`intake.webhooks`](configuration.md#intakewebhooks) section.

- `alertmanager`: Prometheus Alertmanager. See [the adapter](webhooks.md#alertmanager).
- `generic`: Sidekick's own JSON format, for any sender that can post JSON. See [the adapter](webhooks.md#generic).
- `signoz`: SigNoz alerts. See [setting up SigNoz](senders.md#signoz).
- `datadog`: Datadog monitor webhooks. See [setting up Datadog](senders.md#datadog).
- `sentry`: Sentry alerts. See [setting up Sentry](senders.md#sentry).
- `grafana`: Grafana alerts. See [setting up Grafana](senders.md#grafana).

Chat apps start investigations from the messages of the channels that a
[listener](configuration.md#listeners) picks.

- [Slack](slack.md): the app manifest, listeners, mentions and the `/sidekick` command. Its keys are in [the `slack` section](configuration.md#slack).
- [Microsoft Teams](teams.md): the bot, its messaging endpoint, listeners and mentions. Its keys are in [the `teams` section](configuration.md#teams).

## Investigation sources

A source gives an investigation tools to call. Sources are listed once, in
the group of their main use. A source marked "mcp source" is a
`type: mcp` source that points at that product's MCP server; its guide
gives the address, the access it needs and the tools to allow.

Metrics:

- `prometheus`: metrics, alerts and alert rules of Prometheus, Thanos, Mimir and compatible servers. See [the Prometheus source](sources/prometheus.md).
- Datadog (mcp source): logs, metrics, traces, monitors and incidents. See [the Datadog source](sources/datadog.md).
- SigNoz (mcp source): logs, traces, metrics and alerts. See [the SigNoz source](sources/signoz.md).

Logs:

- `elasticsearch`: Elasticsearch and OpenSearch. See [the Elasticsearch and OpenSearch source](sources/elasticsearch.md).

Traces and errors:

- Sentry (mcp source): issues, errors, logs and traces. See [the Sentry source](sources/sentry.md).

Kubernetes and cloud:

- `kubernetes`: cluster events, workload status, problem pods, rollouts and node conditions. See [the Kubernetes source](sources/kubernetes.md).
- `aws`: CloudWatch, CloudTrail, EKS, ECS and AWS Health. See [the AWS source](sources/aws.md).
- `azure`: Log Analytics, Azure Monitor, the Activity Log and AKS. See [the Azure source](sources/azure.md).
- `gcp`: Cloud Logging, Cloud Monitoring, audit logs and GKE. See [the Google Cloud source](sources/gcp.md).

Code and deploys:

- `argocd`: Argo CD applications, their sync status, health, auto-sync and deployment history. See [the Argo CD source](sources/argocd.md).
- `github`: commits, pull requests, releases and GitHub Actions runs. See [the GitHub source](sources/github.md).
- `gitlab`: commits, merge requests, releases, pipelines, deployments and files of GitLab projects, on gitlab.com or self-managed. See [the GitLab source](sources/gitlab.md).

Feature flags:

- `launchdarkly`: who changed a LaunchDarkly flag and what state it is in. See [the LaunchDarkly source](sources/launchdarkly.md).
- `unleash`: who changed an Unleash flag and what state it is in. See [the Unleash source](sources/unleash.md).

Service catalog:

- `backstage`: who owns a service and what it depends on, from a Backstage catalog. See [the Backstage source](sources/backstage.md).

Tickets:

- Linear (mcp source): issues, comments and projects. See [the Linear source](sources/linear.md).

Databases:

- `postgres`: slow statements, running queries, lock waits and replication lag of a Postgres database. See [the Postgres source](sources/postgres.md).

Memory:

- `memory`: past confirmed investigations, lessons and notes about services. See [the memory source](sources/memory.md).

Any MCP server:

- `mcp`: any server that speaks the Model Context Protocol. See [MCP sources](configuration.md#mcp-sources-type-mcp) and [authenticating MCP sources](sources/mcp-authentication.md).

Benchmarks only:

- `mock`: answers tool calls from a scenario file, with no real system. See [mock sources](configuration.md#mock-sources-type-mock) and [the benchmark](benchmark.md#mock-sources).

## Outputs

An output shows an investigation where people look. All their keys are in
[the `outputs` section](configuration.md#outputs).

- `slack`: one Slack message per investigation, edited as it runs. See [what it posts](slack.md#what-it-posts).
- `teams`: the same in a Microsoft Teams thread. See [what it posts](teams.md#what-it-posts).
- `linear.comment` and `jira.comment`: the result as a comment on the ticket the alert is about. See [results on tickets](tickets.md).
- `webhook`: the final result and watcher events as signed JSON. See [webhook callbacks](webhook-callbacks.md).

## Model providers

These are the `provider` values of [the `model` section](configuration.md#model).

- `anthropic`: Claude on the Anthropic API. See [the `anthropic` provider](configuration.md#the-anthropic-provider).
- `bedrock`: models on Amazon Bedrock. See [the `bedrock` provider](configuration.md#the-bedrock-provider).
- `openai`: OpenAI and any server that speaks its API, such as Ollama, vLLM and the llama.cpp server. See [the `openai` provider](configuration.md#the-openai-provider), [local models](configuration.md#local-models-for-free-setups) and [run it for free](free-setups.md).
- `azure`: model deployments on Azure OpenAI and Microsoft Foundry. See [the `azure` provider](configuration.md#the-azure-provider).
- `google`: Gemini on the Gemini API or Vertex AI. See [the `google` provider](configuration.md#the-google-provider).
- `voyage`: embeddings only, for vector memory. See [vector memory](configuration.md#vector-memory-memoryvector).

## Actions

An action is a change Sidekick may make outside itself. By default it makes
one only after a person approves the proposal. The [Actions](actions.md) page
lists every action type with what it does, its credential, whether it can
undo and the mode to use, and helps you choose between them. See [the
`actions` section](configuration.md#actions) for the keys and [action
proposals](web-ui.md#action-proposals) for how people approve them.

- [Chat](actions.md#chat): `slack.reply` and `slack.reaction`.
- [Tickets](actions.md#tickets): `linear.comment`, `linear.issue`, `jira.comment` and `jira.issue`.
- [Monitors changed in the tool](actions.md#monitors-changed-in-the-tool): `datadog.monitor`, `grafana.alert_rule`, `signoz.alert_rule` and `prometheus.rule`.
- [Monitors kept as code](actions.md#monitors-kept-as-code): `github.pull_request` and `gitlab.merge_request`.
- [Code changes and reverts](actions.md#code-changes-and-reverts): `github.code_change`, `gitlab.code_change`, `github.revert` and `gitlab.revert`.
- [Mutes](actions.md#mutes): `alertmanager.silence`, `grafana.silence` and `datadog.downtime`.
- [Rollbacks](actions.md#rollbacks): `kubernetes.rollback`, `helm.rollback`, `argocd.rollback`, `aws.ecs_rollback`, `aws.lambda_rollback`, `gcp.cloud_run_rollback` and `azure.container_app_rollback`.
- [Feature flags](actions.md#feature-flags): `launchdarkly.flag` and `unleash.flag`.
