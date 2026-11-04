# Actions

An action is a change Sidekick may make outside itself, such as a comment on
a ticket, a pull request or a rollback. Nothing runs unless the action is
defined in the [`actions` section](configuration.md#actions) with its own
credential and limits, and a route lists it. The default is a person's
approval: the model proposes, and an approver accepts or rejects each
proposal in Slack or in the web UI. See [action modes](configuration.md#action-modes)
for the other modes and [action proposals](web-ui.md#action-proposals) for
what an approver sees. To add an action type of your own, read [Writing an
action](plugins/actions.md).

This page lists every action type by family, helps you choose between them,
and gathers the safety rules. Each type links to its section of the
configuration reference, which has the keys, the limits and the full rules.

In the tables, **Undo** says whether an approver can undo a change that
worked. **Mode** says how to use the action on a route: every action is for
`mode: approved`, and "needs `allowReadWrite`" means a `read-write` route may
list it only when the action sets `allowReadWrite: true`.

## Chat

These post as the bot of the [`slack`](configuration.md#slack) section, so
they need no credential of their own.

| Type | What it does | Credential | Undo | Mode |
|---|---|---|---|---|
| [`slack.reply`](configuration.md#slackreply-and-slackreaction) | Posts a reply in the Slack thread of the investigation. | The Slack bot | No | Approved |
| [`slack.reaction`](configuration.md#slackreply-and-slackreaction) | Adds an emoji to the Slack message that carried the alert. | The Slack bot | No | Approved |

## Tickets

| Type | What it does | Credential | Undo | Mode |
|---|---|---|---|---|
| [`linear.comment`](configuration.md#linearcomment-and-linearissue) | Adds a comment to a Linear issue. | A Linear API key | No | Approved |
| [`linear.issue`](configuration.md#linearcomment-and-linearissue) | Creates a Linear issue. | A Linear API key | No | Approved |
| [`jira.comment`](configuration.md#jiracomment-and-jiraissue) | Adds a comment to a Jira issue. | An email and an API token (Jira Cloud) | No | Approved |
| [`jira.issue`](configuration.md#jiracomment-and-jiraissue) | Creates a Jira issue. | An email and an API token (Jira Cloud) | No | Approved |

## Monitors changed in the tool

These change a monitor or alert rule in place through the tool's API. The
change is live as soon as it is approved, and there is no review in Git.

| Type | What it does | Credential | Undo | Mode |
|---|---|---|---|---|
| [`datadog.monitor`](configuration.md#datadogmonitor) | Changes or deletes a Datadog monitor. | A Datadog API key and application key | Yes: puts back only the fields Sidekick changed. A delete cannot be undone. | Approved |
| [`grafana.alert_rule`](configuration.md#grafanaalert_rule) | Changes or deletes a Grafana alert rule. | A Grafana service account token | Yes: puts back only the fields Sidekick changed. A delete cannot be undone. | Approved |
| [`signoz.alert_rule`](configuration.md#signozalert_rule) | Changes or deletes a SigNoz alert rule. | A SigNoz API key | Yes: puts back only the fields Sidekick changed. A delete cannot be undone. | Approved |
| [`prometheus.rule`](configuration.md#prometheusrule) | Changes rules of a `PrometheusRule` resource in the cluster Sidekick runs in. | A Kubernetes service account | Yes: puts back only the fields Sidekick changed. | Approved |

If Argo CD, Flux or Helm own a `PrometheusRule`, they undo a live change at
their next sync. Use `github.pull_request` for those. Keep rules that
Terraform or another tool manages out of the limits of `signoz.alert_rule`.

## Monitors kept as code

These open a pull request or merge request that changes monitor files kept in
Git. A person reviews it before anything is merged.

| Type | What it does | Credential | Undo | Mode |
|---|---|---|---|---|
| [`github.pull_request`](configuration.md#githubpull_request) | Opens a pull request that changes monitor definitions kept in Git. | A GitHub App of its own | Yes: closes the pull request, or opens a revert pull request if it was merged. | Approved |
| [`gitlab.merge_request`](configuration.md#gitlabmerge_request) | Opens a merge request that changes monitor definitions kept in Git. | A GitLab token of its own | Yes: closes the merge request, or opens a revert merge request if it was merged. | Approved |

## Code changes and reverts

These open a draft pull request or merge request, which Sidekick never
merges. The repository's own checks run on it, and Sidekick reports their
result in the thread (see [the CI
report](configuration.md#ci-report-of-a-pull-request-or-merge-request)). A
code change carries new file content from the model; a revert only names what
to revert.

| Type | What it does | Credential | Undo | Mode |
|---|---|---|---|---|
| [`github.code_change`](configuration.md#githubcode_change) | Opens a draft pull request that changes application code. | A GitHub App of its own | Yes: closes the pull request, or opens a draft revert pull request if it was merged. | Approved |
| [`gitlab.code_change`](configuration.md#gitlabcode_change) | Opens a draft merge request that changes application code. | A GitLab token of its own | Yes: closes the merge request, or opens a draft revert merge request if it was merged. | Approved |
| [`github.revert`](configuration.md#githubrevert) | Opens a draft pull request that reverts one commit or one merged pull request. | A GitHub App of its own | Yes: closes the pull request, or opens a draft pull request that puts back what the commit wrote. | Approved |
| [`gitlab.revert`](configuration.md#gitlabrevert) | Opens a draft merge request that reverts one commit or one merged merge request. | A GitLab token of its own | Yes: closes the merge request, or opens a draft merge request that puts back what the commit wrote. | Approved |

## Mutes

A mute holds back notifications for a limited time. It does not change the
monitor or close the alert. None of these needs `allowReadWrite`.

| Type | What it does | Credential | Undo | Mode |
|---|---|---|---|---|
| [`alertmanager.silence`](configuration.md#alertmanagersilence) | Silences alerts in Alertmanager for a limited time. | A bearer token or basic auth of its own | Yes: ends the silence early. | Approved |
| [`grafana.silence`](configuration.md#grafanasilence) | Silences alerts in the Alertmanager built into Grafana for a limited time. | A Grafana service account token | Yes: ends the silence early. | Approved |
| [`datadog.downtime`](configuration.md#datadogdowntime) | Mutes the notifications of a Datadog monitor for a limited time with a downtime. | A Datadog API key and application key | Yes: cancels the downtime early. | Approved |

## Rollbacks

A rollback changes a running system, so every rollback is approval-only. The
approver sees the revision that runs now and the one it goes back to, and
Sidekick does not wait for the service to recover: the
[watcher](configuration.md#watchers) reports it.

| Type | What it does | Credential | Undo | Mode |
|---|---|---|---|---|
| [`kubernetes.rollback`](configuration.md#kubernetesrollback) | Puts a Deployment, StatefulSet or DaemonSet back on an earlier revision. | A Kubernetes service account | Yes: puts back the revision that ran before. | Approved; needs `allowReadWrite` |
| [`helm.rollback`](configuration.md#helmrollback) | Puts a Helm release back on an earlier revision. | A Kubernetes service account | Yes: rolls back to the revision that ran before. | Approved; needs `allowReadWrite` |
| [`argocd.rollback`](configuration.md#argocdrollback) | Puts an Argo CD application back on an earlier deployed revision. It refuses an application with auto-sync on. | An Argo CD API token | Yes: a second rollback to the earlier revision. | Approved; needs `allowReadWrite` |
| [`aws.ecs_rollback`](configuration.md#awsecs_rollback) | Puts an Amazon ECS service back on an earlier task definition revision. | An IAM role | Yes: puts the service back on the task definition it ran. | Approved; needs `allowReadWrite` |
| [`aws.lambda_rollback`](configuration.md#awslambda_rollback) | Points an AWS Lambda alias back at the published version it served before. | An IAM role | Yes: points the alias back at its old version and split. | Approved; needs `allowReadWrite` |
| [`gcp.cloud_run_rollback`](configuration.md#gcpcloud_run_rollback) | Sends the traffic of a Cloud Run service back to an earlier ready revision. | A Google credential with a custom role | Yes: puts the traffic list back as it was. | Approved; needs `allowReadWrite` |
| [`azure.container_app_rollback`](configuration.md#azurecontainer_app_rollback) | Moves all the traffic of an Azure Container App back to an earlier revision. | An Azure service principal, or Workload Identity on AKS | Yes: puts the traffic back. | Approved; needs `allowReadWrite` |

## Feature flags

| Type | What it does | Credential | Undo | Mode |
|---|---|---|---|---|
| [`launchdarkly.flag`](configuration.md#launchdarklyflag) | Turns a LaunchDarkly flag off in one environment. | A LaunchDarkly API token | Yes: turns the flag back on. | Approved; needs `allowReadWrite` |
| [`unleash.flag`](configuration.md#unleashflag) | Turns an Unleash flag off in one environment, or undoes its newest change. | An Unleash API token | Yes: puts back the change. | Approved; needs `allowReadWrite` |

## Which action when

- **A bad deploy that Flux, or Argo CD with auto-sync, rolled out:** a revert
  pull request or merge request ([`github.revert`](configuration.md#githubrevert),
  [`gitlab.revert`](configuration.md#gitlabrevert)). The controller would put
  back any change made to the cluster by hand.
- **A bad deploy in an Argo CD application without auto-sync:**
  [`argocd.rollback`](configuration.md#argocdrollback), which refuses an
  application with auto-sync on.
- **A Kubernetes workload that nothing else manages:**
  [`kubernetes.rollback`](configuration.md#kubernetesrollback).
- **A Helm release that no GitOps controller owns:**
  [`helm.rollback`](configuration.md#helmrollback).
- **A known noisy alert during a maintenance window:** a mute
  ([`alertmanager.silence`](configuration.md#alertmanagersilence),
  [`grafana.silence`](configuration.md#grafanasilence) or
  [`datadog.downtime`](configuration.md#datadogdowntime)).
- **A false positive:** a monitor fix, either in the tool (for example
  [`datadog.monitor`](configuration.md#datadogmonitor)) or in Git
  ([`github.pull_request`](configuration.md#githubpull_request),
  [`gitlab.merge_request`](configuration.md#gitlabmerge_request)). A rollback
  fixes the cause of an incident, never a noisy monitor.
- **A bug in code:** a code change
  ([`github.code_change`](configuration.md#githubcode_change),
  [`gitlab.code_change`](configuration.md#gitlabcode_change)), as a draft that
  the repository's own checks run on.

## Safety rules

- **Approval-only actions.** An action that changes a running system, such as
  a rollback or a flag change, may be listed on a `read-write` route only when
  its definition sets `allowReadWrite: true`. See [action
  modes](configuration.md#action-modes).
- **Both revisions are shown.** For a rollback, the approver sees the
  revision that runs now and the one it goes back to. See
  [`kubernetes.rollback`](configuration.md#kubernetesrollback) and [action
  proposals](web-ui.md#action-proposals).
- **A deploy after the proposal makes the run fail.** If someone deployed
  since the proposal, the rollback fails as a conflict and a new proposal is
  needed. See [`kubernetes.rollback`](configuration.md#kubernetesrollback),
  [`argocd.rollback`](configuration.md#argocdrollback) and
  [`helm.rollback`](configuration.md#helmrollback).
- **Undo.** An approver can undo a change that worked, where the Undo column
  says yes. See [action proposals](web-ui.md#action-proposals).
- **Drafts that Sidekick never merges.** Code changes and reverts are always
  drafts, and Sidekick never merges them. See
  [`github.code_change`](configuration.md#githubcode_change) and
  [`gitlab.code_change`](configuration.md#gitlabcode_change).
- **The watcher reports recovery.** A rollback does not wait for the alert to
  recover; the [watcher](configuration.md#watchers) reports whether it does.
- **Limits are checked twice.** Sidekick checks the parameters against the
  action's limits when the model proposes and again when the proposal runs.
  See [the `actions` section](configuration.md#actions).

## Read sources that pair with each family

An investigation reads with a source before it proposes. These are the
sources to set up for each family.

- Rollbacks, Prometheus rules and Argo CD: [Kubernetes](sources/kubernetes.md),
  [Argo CD](sources/argocd.md), [Prometheus](sources/prometheus.md).
- Cloud rollbacks: [AWS](sources/aws.md), [Google Cloud](sources/gcp.md),
  [Azure](sources/azure.md).
- Pull requests, merge requests, code changes and reverts:
  [GitHub](sources/github.md), [GitLab](sources/gitlab.md).
- Feature flags: [LaunchDarkly](sources/launchdarkly.md),
  [Unleash](sources/unleash.md).
- Monitors and mutes: [Datadog](sources/datadog.md),
  [SigNoz](sources/signoz.md), [Prometheus](sources/prometheus.md).
- Tickets: [Linear](sources/linear.md).
