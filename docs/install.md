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

Write down how long the steps take; it helps us make the quick start
shorter.

### 1. Create the Slack app

Create a Slack app for your workspace, add the bot token scope
`chat:write`, install the app, and copy its bot token (`xoxb-...`). Invite
the app to the channel Sidekick will post in, such as `#alerts`.
[Investigations in Slack](slack.md#set-up-the-slack-app) has the details.

### 2. Create the Secrets

```sh
kubectl create namespace sidekick
kubectl -n sidekick create secret generic sidekick-anthropic --from-literal=apiKey='<Anthropic API key>'
kubectl -n sidekick create secret generic sidekick-slack --from-literal=botToken='<xoxb-... bot token>'
kubectl -n sidekick create secret generic sidekick-webhook --from-literal=token="$(openssl rand -hex 32)"
```

`sidekick-webhook` holds the bearer token that senders such as
Alertmanager must send to Sidekick's webhook endpoint.

### 3. Install the chart

```sh
helm install sidekick oci://ghcr.io/nourtalaat/charts/sidekick --version 0.1.0 \
  --namespace sidekick \
  -f https://raw.githubusercontent.com/nourtalaat/sidekick/v0.1.0/charts/sidekick/quickstart-values.yaml \
  --set config.outputs.slack.channel='#alerts'
kubectl -n sidekick rollout status deploy/sidekick
```

The [quick-start values](../charts/sidekick/quickstart-values.yaml) run
one replica with the bundled Postgres, the `claude-opus-5-5` model, an
Alertmanager webhook endpoint named `alertmanager`, and a `default` route
that posts to the Slack channel.

### 4. Check the install

```sh
kubectl -n sidekick exec deploy/sidekick -c sidekick -- sidekick doctor
```

[`sidekick doctor`](running.md#checking-an-install-with-sidekick-doctor)
checks the database, the Slack token, the API key and every source, and
prints a fix for anything wrong.

### 5. Send a test alert

```sh
kubectl -n sidekick exec deploy/sidekick -c sidekick -- sidekick test-alert
```

Sidekick answers `202 Accepted`, posts "Looking into it" in the channel
within seconds, and edits the message with its verdict within minutes.
[Sending a test alert](webhooks.md#sending-a-test-alert) describes the
alert and the flags.

### 6. Send real alerts

Point an Alertmanager receiver at the endpoint, with the token from
`sidekick-webhook` mounted into Alertmanager:

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

Sidekick needs no cloud keys in a Secret on EKS or GKE: the pod gets an
identity through its service account, which the chart creates and names
after the release (`sidekick` for a release named `sidekick`). The
[Bedrock connector](configuration.md#the-bedrock-provider) uses the AWS
identity. The identity applies to every container in the pod, so
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

**Other annotations**, such as `azure.workload.identity/client-id` for
Azure Workload Identity, go under `serviceAccount.annotations`. Azure also
needs the pod label `azure.workload.identity/use: "true"` in `podLabels`.
An annotation that `serviceAccount.workloadIdentity` already sets is
rejected, so each one has one source.

EKS adds the identity to a pod when the pod starts, so after setting or
changing the role or the association, restart the pods with
`kubectl -n sidekick rollout restart deploy/sidekick`; a `helm upgrade`
that changes only the service account does not replace them. The token
volumes that EKS adds are separate from the Kubernetes API token, so
`serviceAccount.automountToken` can stay `false`. With
`serviceAccount.create: false`, the chart uses the service account named
in `serviceAccount.name`; annotate it yourself, since
`serviceAccount.workloadIdentity` is then rejected.

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
