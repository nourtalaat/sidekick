# Installing Sidekick

Sidekick installs on Kubernetes with one Helm chart. This page walks
through the quick start, which runs everything in one namespace with a
bundled Postgres for evaluation, and then the changes for production.

## What you need

- Kubernetes 1.29 or newer, and `kubectl` pointed at it.
- Helm 3.8 or newer, for charts in OCI registries.
- A Slack workspace where you can create an app.
- An Anthropic API key.

## Quick start

Five steps, about 13 minutes in all. The times are for someone who has
the Slack and Anthropic accounts ready; write down yours, it helps us make
the quick start shorter.

### 1. Create the Slack app (3 minutes)

Create an internal Slack app from the manifest in
[`deploy/slack/manifest.yaml`](../deploy/slack/manifest.yaml): in
[api.slack.com/apps](https://api.slack.com/apps), choose **Create New App**,
**From a manifest**, pick your workspace and paste the manifest. Install
the app, copy its bot token (`xoxb-...`), and under **Basic Information**
create an app-level token with the `connections:write` scope (`xapp-...`).
Sidekick uses it to open a Socket Mode connection, so it needs no public
URL. [Investigations in Slack](slack.md#set-up-the-slack-app) has the
details.

### 2. Create the Secrets (2 minutes)

```sh
kubectl create namespace sidekick
kubectl -n sidekick create secret generic sidekick-anthropic --from-literal=apiKey='<Anthropic API key>'
kubectl -n sidekick create secret generic sidekick-slack \
  --from-literal=botToken='<xoxb-... bot token>' --from-literal=appToken='<xapp-... app-level token>'
```

### 3. Install the chart (4 minutes)

```sh
helm install sidekick oci://ghcr.io/nourtalaat/charts/sidekick --version 0.1.0 \
  --namespace sidekick \
  -f https://raw.githubusercontent.com/nourtalaat/sidekick/v0.1.0/charts/sidekick/quickstart-values.yaml \
  --set config.listeners.alerts.channel='#alerts'
kubectl -n sidekick rollout status deploy/sidekick
```

The [quick-start values](../charts/sidekick/quickstart-values.yaml) run
one replica with the bundled Postgres and the `claude-opus-5-5` model, and
turn on:

- a Kubernetes source that reads the cluster Sidekick runs in, with the
  read-only roles the chart creates for it
  ([the Kubernetes source](sources/kubernetes.md#with-the-helm-chart));
- one Slack listener, `alerts`, on the channel you gave: every message that
  a bot posts there starts an investigation, answered in its thread;
- the `default` route, which uses the Kubernetes source and the `slack`
  output.

The `slack` output has a channel of its own, `#alerts`, which Sidekick uses
only for alerts that do not come from Slack, such as webhooks (see
[Next steps](#next-steps)); add `--set config.outputs.slack.channel=...`
when you use another one.

### 4. Invite the app and check the install (1 minute)

In Slack, invite the app to the channel: `/invite @Sidekick`. Then:

```sh
kubectl -n sidekick exec deploy/sidekick -c sidekick -- sidekick doctor
```

[`sidekick doctor`](running.md#checking-an-install-with-sidekick-doctor)
checks the database, the Slack token, the API key and every source, and
prints a fix for anything wrong:

```text
ok    database: connected; the schema has every migration this release needs (33)
ok    slack: the bot token works: it is the bot user sidekick in the workspace Acme
ok    model claude: the anthropic API accepted the key
ok    prices: every configured model has a price
ok    source k8s: connected; 5 of 5 tools exposed
```

### 5. Send a test alert (3 minutes)

```sh
kubectl -n sidekick exec deploy/sidekick -c sidekick -- sidekick test-alert --channel '#alerts'
```

The command posts a sample alert into the channel, as the bot, about a pod
of your cluster (or a made-up pod, which the message says, when the
Kubernetes source cannot list one), and prints which listener takes it:

```text
Posted a test alert in #alerts, about the pod shop/api-5d8c9b7f4-m2xkq.
The listener alerts takes it. The reply appears in its thread within 30 seconds, and the verdict within a few minutes.
```

Sidekick answers in the alert's thread with "Looking into it" within
seconds, then edits the message to show its steps and, within minutes, its
verdict. The pod is probably fine, so a "False alarm" is the likely and
right answer. You can also mention `@Sidekick` on any message.
[Sending a test alert](webhooks.md#sending-a-test-alert) describes the
alert and the flags, and the form that sends it to a webhook endpoint.

### Next steps

- **Let your monitoring bot's alerts in.** The `alerts` listener takes
  every bot message in its channel. Narrow it with `match.sender.names` or
  `match.text`, and use `extract` to read the alert's name and service, as
  [the `listeners` section](configuration.md#listeners) shows.
- **Send alerts from Alertmanager.** Create a Secret with a bearer token,
  add it to `secrets`, and add a webhook endpoint:

  ```sh
  kubectl -n sidekick create secret generic sidekick-webhook --from-literal=token="$(openssl rand -hex 32)"
  ```

  ```yaml
  secrets: [sidekick-anthropic, sidekick-slack, sidekick-webhook]
  config:
    intake:
      webhooks:
        alertmanager:
          adapter: alertmanager
          auth: { bearer: { name: sidekick-webhook, key: token } }
  ```

  Point an Alertmanager receiver at it, with the token mounted into
  Alertmanager:

  ```yaml
  receivers:
    - name: sidekick
      webhook_configs:
        - url: http://sidekick.sidekick.svc:8080/hooks/alertmanager
          send_resolved: true
          http_config:
            authorization:
              credentials_file: /etc/alertmanager/secrets/sidekick-webhook/token
  ```

- **Add more sources.** [The `sources` section](configuration.md#sources)
  and the pages under `docs/sources` show how to add logs, metrics, code
  and tickets.

## Verifying the image

Each release image is signed with cosign keyless signing in the release
workflow, and has a signed SPDX SBOM. Check both before you install:

```sh
cosign verify ghcr.io/nourtalaat/sidekick:0.1.0 \
  --certificate-identity https://github.com/nourtalaat/sidekick/.github/workflows/release.yml@refs/tags/v0.1.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation ghcr.io/nourtalaat/sidekick:0.1.0 --type spdxjson \
  --certificate-identity https://github.com/nourtalaat/sidekick/.github/workflows/release.yml@refs/tags/v0.1.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The image is built for linux/amd64 and linux/arm64 on a distroless base.
It runs as user 65532 with no shell, and the chart runs it with a
read-only root filesystem and an `emptyDir` at `/tmp`. `LICENSE` and
`NOTICE` are in `/usr/share/doc/sidekick/`. The validator image,
`ghcr.io/nourtalaat/sidekick-validator`, is signed the same way; see
[The monitor validator](validator.md#verifying-the-image).

## Production

Start from the quick-start values and change these:

- **Database.** The bundled Postgres is one pod with no backups. Use a
  managed Postgres 15 or newer, put its URL in a Secret, list the Secret
  under `secrets`, and set `config.database.urlSecret`. Leave
  `postgres.enabled` off.
- **Replicas.** Run 2 or more (`replicaCount`, the default is 2) and keep
  `pdb.enabled`, so a node drain always leaves one running.
- **Migrations.** `migrations.mode` is `startup` by default: each replica
  migrates when it starts, one at a time. With `job`, a Job runs
  `sidekick migrate up` before each install and upgrade, and Sidekick runs
  with `database.migrations: manual`. The Job reads only the database
  Secret, which must exist before the install, so it cannot come from
  `externalSecrets`. With `manual`, run `sidekick migrate up` yourself
  before each upgrade; pods are not ready until the schema is current.
- **Secrets.** `externalSecrets` makes Secrets with the External Secrets
  Operator and mounts them, so nothing is created by hand.
- **Cloud credentials.** Give the pod a cloud identity instead of stored
  keys; see [Cloud credentials](#cloud-credentials).
- **Network.** `networkPolicy.httpFrom` limits who can reach the webhook
  port. `networkPolicy.restrictEgress` limits where Sidekick connects; list
  Slack, the model provider and each source under `networkPolicy.egress`.
- **Metrics.** `metrics.serviceMonitor.enabled` makes a ServiceMonitor for
  the Prometheus Operator.
- **Webhooks from outside the cluster.** `ingress` or `httpRoute` expose
  `/hooks/` only. Keep the metrics port off the public network.
- **Microsoft Teams.** The Teams bot's messaging endpoint,
  `/teams/messages` on the webhook port, needs a public https URL that the
  Bot Framework can reach. The chart adds no Ingress for it by default;
  add the path to `ingress.hosts`, as
  [Reaching the messaging endpoint](teams.md#reaching-the-messaging-endpoint)
  shows.

### MCP servers in sidecars

An MCP server that only speaks stdio runs in a sidecar behind a bridge
that serves it as streamable HTTP on localhost, such as
[supergateway](https://github.com/supercorp-ai/supergateway):

```yaml
mcpSidecars:
  - name: git
    image: supercorp/supergateway:<pinned tag>
    args: [--stdio, "uvx mcp-server-git --repository /work/repo", --outputTransport, streamableHttp,
           --host, 127.0.0.1, --port, "8101", --healthEndpoint, /healthz, --healthCheck, server]
    env: [{ name: HOME, value: /tmp }]
    port: 8101
    healthPath: /healthz
    resources: { requests: { cpu: 50m, memory: 128Mi }, limits: { memory: 256Mi } }

config:
  sources:
    git:
      type: mcp
      transport: http
      url: http://127.0.0.1:8101/mcp
      tools: { allow: [git_log, git_show, git_diff] }
```

Each sidecar runs with Sidekick's security context and gets its own
`emptyDir` at `/tmp`. `healthPath` and `port` give the sidecar a startup
probe, which allows 5 minutes for servers that download packages, and a
liveness probe, which restarts a bridge that stops answering. A sidecar
has no readiness probe: one that is down makes only its source unavailable
and never takes Sidekick out of service. Pin the image tag; `latest` is
rejected.

### Cloud credentials

Sidekick needs no cloud keys in a Secret on EKS, GKE or AKS: the pod gets an
identity through its service account, which the chart creates and names
after the release (`sidekick` for a release named `sidekick`). The
[Bedrock connector](configuration.md#the-bedrock-provider) and the
[AWS source](sources/aws.md) use the AWS identity, and the [Google connector](configuration.md#the-google-provider)
with `backend: vertex` uses the Google one. The identity applies to every container in the pod, so
[MCP servers in sidecars](#mcp-servers-in-sidecars) can use it too.

**EKS IRSA** (IAM roles for service accounts). The cluster needs an IAM
OIDC provider, and the role's trust policy must let
`system:serviceaccount:<namespace>:<service account>` call
`sts:AssumeRoleWithWebIdentity`. Then set the role:

```yaml
serviceAccount:
  workloadIdentity:
    awsRoleArn: arn:aws:iam::111122223333:role/sidekick-bedrock
```

The chart sets the `eks.amazonaws.com/role-arn` annotation, and EKS adds
the role and a token file to the pod.

**EKS Pod Identity.** Nothing changes in the chart. Install the EKS Pod
Identity Agent add-on, let `pods.eks.amazonaws.com` call `sts:AssumeRole`
and `sts:TagSession` in the role's trust policy, and link the role to the
service account:

```sh
aws eks create-pod-identity-association --cluster-name <cluster> \
  --namespace sidekick --service-account sidekick \
  --role-arn arn:aws:iam::111122223333:role/sidekick-bedrock
```

With `networkPolicy.restrictEgress`, allow the agent at
`169.254.170.23/32` on port 80.

**GKE Workload Identity.** With Workload Identity Federation on the
cluster, let the Kubernetes service account act as a Google service
account:

```sh
gcloud iam service-accounts add-iam-policy-binding \
  sidekick@<project>.iam.gserviceaccount.com \
  --role roles/iam.workloadIdentityUser \
  --member "serviceAccount:<project>.svc.id.goog[sidekick/sidekick]"
```

Then set it in the chart, which adds the `iam.gke.io/gcp-service-account`
annotation:

```yaml
serviceAccount:
  workloadIdentity:
    gcpServiceAccount: sidekick@<project>.iam.gserviceaccount.com
```

**AKS Workload Identity.** Turn on the OIDC issuer and workload identity
on the cluster, then let the Kubernetes service account use a
user-assigned managed identity (or a Microsoft Entra application) through
a federated credential:

```sh
az identity federated-credential create --name sidekick \
  --identity-name sidekick --resource-group <group> \
  --issuer "$(az aks show -g <group> -n <cluster> --query oidcIssuerProfile.issuerUrl -o tsv)" \
  --subject system:serviceaccount:sidekick:sidekick \
  --audiences api://AzureADTokenExchange
```

Then set the identity's client ID in the chart:

```yaml
serviceAccount:
  workloadIdentity:
    azureClientId: 00000000-1111-2222-3333-444444444444
    # azureTenantId: only when the identity is in another tenant
```

The chart sets the `azure.workload.identity/client-id` annotation (and
`azure.workload.identity/tenant-id` when `azureTenantId` is set) and gives
the Sidekick pods the label `azure.workload.identity/use: "true"`. The AKS
webhook adds a token file and the `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` and
`AZURE_FEDERATED_TOKEN_FILE` variables only to pods with that label. With
`networkPolicy.restrictEgress`, allow `login.microsoftonline.com` on port
443.

The [Azure connector](configuration.md#the-azure-provider) uses this
identity with `auth: entra`. Give the managed identity the Cognitive
Services OpenAI User role on the Azure OpenAI or Foundry resource.

**Other annotations** go under `serviceAccount.annotations`. An
annotation that `serviceAccount.workloadIdentity` already sets is
rejected, and so is `azure.workload.identity/use` in `podLabels` when
`azureClientId` is set, so each one has one source.

EKS and AKS add the identity to a pod when the pod starts, so after
setting or changing the role, the association or the client ID, restart
the pods with `kubectl -n sidekick rollout restart deploy/sidekick`; a
`helm upgrade` that changes only the service account does not replace
them. The token volumes that EKS and AKS add are separate from the
Kubernetes API token, so `serviceAccount.automountToken` can stay
`false`. With
`serviceAccount.create: false`, the chart uses the service account named
in `serviceAccount.name`; annotate it yourself, since
`serviceAccount.workloadIdentity` is then rejected. For Azure, also set
the `azure.workload.identity/use: "true"` label in `podLabels`.

## Values

The main values; [values.yaml](../charts/sidekick/values.yaml) describes
every one, and the chart rejects unknown keys.

| Value | Default | Meaning |
|---|---|---|
| `config` | `{}` | `sidekick.yaml`; see [Configuration](configuration.md). The chart sets `server.listen`, `server.metricsListen`, `secrets.dir` and `database.migrations`. |
| `secrets` | `[]` | Secrets mounted at `/var/run/secrets/sidekick/<name>`. |
| `externalSecrets` | `[]` | ExternalSecrets to make and mount. |
| `migrations.mode` | `startup` | `startup`, `job` or `manual`. |
| `postgres.enabled` | `false` | The bundled Postgres, for evaluation only. |
| `replicaCount` | `2` | Sidekick pods. |
| `image.tag`, `image.digest` | the chart's version | The image; a digest wins over the tag. |
| `resources` | 100m CPU, 256Mi requested; 512Mi limit | `GOMEMLIMIT` is set to 90% of the memory limit. |
| `serviceAccount.workloadIdentity.awsRoleArn` | `""` | The IAM role for EKS IRSA. |
| `serviceAccount.workloadIdentity.gcpServiceAccount` | `""` | The Google service account for GKE Workload Identity. |
| `serviceAccount.workloadIdentity.azureClientId` | `""` | The client ID of the identity for AKS Workload Identity; also adds the `azure.workload.identity/use` pod label. |
| `serviceAccount.workloadIdentity.azureTenantId` | `""` | That identity's tenant, when it is not the cluster's. |
| `serviceAccount.annotations` | `{}` | More service account annotations. |
| `mcpSidecars` | `[]` | stdio MCP servers behind a bridge: `name`, `image`, `args`, `env`, `port`, `healthPath`, `resources`, `volumeMounts`. |
| `networkPolicy.enabled` | `true` | Network policies for Sidekick and the bundled Postgres. |
| `rbac.kubernetesSource` | `true` | Read-only roles for each Kubernetes source that reads this cluster, made from its `namespaces` and `logs`; see [The Kubernetes source](sources/kubernetes.md#with-the-helm-chart). |
| `validator.enabled`, `validator.mode` | `false`, `sidecar` | The monitor validator, as a sidecar or as Jobs; see [The monitor validator](validator.md). |
| `pdb.enabled` | `true` | A PodDisruptionBudget with `pdb.minAvailable`. |
| `metrics.serviceMonitor.enabled` | `false` | A ServiceMonitor. |
| `ingress.enabled`, `httpRoute.enabled` | `false` | Expose `/hooks/` outside the cluster. |

The pod runs with `startupProbe` on `/healthz`, which allows 2 minutes for
start-up migrations, `livenessProbe` on `/healthz` and `readinessProbe` on
`/readyz`, all on the metrics port; see
[Running Sidekick](running.md#endpoints).

## Upgrading

```sh
helm upgrade sidekick oci://ghcr.io/nourtalaat/charts/sidekick --version <new version> \
  --namespace sidekick --reuse-values
```

Pods are replaced one at a time, with a new one ready before an old one
stops. A stopping pod hands its running investigations back to the queue,
and another pod resumes them. A config change rolls the pods too. The
release notes say when an upgrade needs more, such as a backup before a
migration.

## Uninstalling

```sh
helm uninstall sidekick --namespace sidekick
```

The bundled Postgres's data volume and its password Secret are kept, so a
new install finds the data. To remove them too:

```sh
kubectl -n sidekick delete pvc data-sidekick-postgres-0
kubectl -n sidekick delete secret sidekick-postgres
```
