# The monitor validator

Before Sidekick opens a pull request that changes monitor definitions, it
checks every changed file with the validator. A file that fails is not
committed: the errors go back to the model, which can fix the change, and
no pull request is opened.

> **Status:** the validator image and the chart settings are ready; the
> pull request action that sends files to it comes next.

The validator is `sidekick-validate`, in its own image,
`ghcr.io/nourtalaat/sidekick-validator`. It carries the tools the checks
need, so the main Sidekick image stays small and holds none of them:

| Tool | Version | License | Source |
|---|---|---|---|
| promtool (Prometheus) | 3.15.0 | Apache-2.0 | [prometheus/prometheus](https://github.com/prometheus/prometheus/tree/v3.15.0) |
| OpenTofu | 1.13.1 | MPL-2.0 | [opentofu/opentofu](https://github.com/opentofu/opentofu/tree/v1.13.1) |
| Datadog provider | 4.25.0 | MPL-2.0 | [DataDog/terraform-provider-datadog](https://github.com/DataDog/terraform-provider-datadog/tree/v4.25.0) |
| SigNoz provider | 0.1.5 | MPL-2.0 | [SigNoz/terraform-provider-signoz](https://github.com/SigNoz/terraform-provider-signoz/tree/v0.1.5) |
| Grafana provider | 4.49.0 | MPL-2.0 | [grafana/terraform-provider-grafana](https://github.com/grafana/terraform-provider-grafana/tree/v4.49.0) |

Each Sidekick release ships a validator image with the same version
number. `/usr/share/doc/sidekick-validate/NOTICE` in the image lists the
licenses of these tools and of every Go module linked into them and into
`sidekick-validate`.

## Formats

Every path a pull request action may change has a format. The validator
checks each format this way:

| Format | Check | Notes |
|---|---|---|
| `prometheus_rules` | `promtool check rules` | One file at a time. Errors give the file and line. |
| `terraform` | `tofu init -backend=false`, then `tofu validate` | One module directory at a time, with the module's other files. No backend is touched. HashiCorp Terraform is under the Business Source License, which is not open source, so the image uses OpenTofu, which reads the same configuration language. |
| `grafana_alert_file` | Sidekick's JSON Schema of the Grafana alerting file provisioning format, then each Prometheus query parsed as PromQL | Grafana offers no schema or offline check for these files. The schema checks the fields Grafana documents as required: a group's `name`, `folder`, `interval` and `rules`, and a rule's `uid`, `title`, `condition`, `data` and `for`. YAML and JSON files both work. |

In Grafana files, a query is parsed as PromQL when its
`model.datasource.type` is `prometheus` (or a managed Prometheus such as
`grafana-amazonprometheus-datasource`), or when it names no type and has
an `expr`. Grafana's own expressions (`datasourceUid: __expr__`) are not
queries. A query for any other datasource, such as Loki, cannot be
checked, so the result lists it under `notChecked` rather than counting
it as passed. Grafana's `$__interval`, `$__rate_interval` and `$__range`
variables are allowed in queries.

## Providers and the mirror

`tofu validate` needs the providers a module uses. The image carries them
in a provider mirror for its own platform (amd64 or arm64), and OpenTofu
installs providers from that mirror only, so validation needs no network.
The mirror serves each provider under both `registry.opentofu.org` and
`registry.terraform.io`, so modules and lock files written with Terraform
work as they are.

When a module's `.terraform.lock.hcl` asks for a provider version the
mirror does not have, validation fails before OpenTofu runs:

```text
registry.terraform.io/datadog/datadog 3.0.0: provider version not in the validator image, which has 4.25.0. Lock a version the image has, or set validator.allowDownloads to let the validator download it.
```

A module with no lock file gets the newest mirrored version its version
constraints allow. When the constraints allow none of them, `tofu init`
fails, and the error starts with the same "provider version not in the
validator image".

`validator.allowDownloads: true` lets OpenTofu download providers that
are not in the mirror, and remote modules, from their registries over
HTTPS. Without it, modules can only use local modules (`source =
"../modules/..."`); remote ones fail in `tofu init`.

## Turning it on

The validator is off by default. Turn it on in the chart values:

```yaml
validator:
  enabled: true
  mode: sidecar          # or job
  allowDownloads: false
```

### Sidecar mode

The default. Each Sidekick pod gets a `validator` container that listens
on `127.0.0.1:8200`, so only the containers of the same pod reach it. It
runs as user 65532 with a read-only root filesystem, and keeps the files
of each validation in its own `emptyDir` at `/tmp`
(`validator.workDirSizeLimit`, 2Gi by default). Its startup and liveness
probes run `sidekick-validate health` in the container. It has no
readiness probe, so a validator that is down makes validations fail and
never takes Sidekick out of service.

**A NetworkPolicy cannot isolate the sidecar.** Kubernetes applies a
NetworkPolicy to a whole pod, and the sidecar shares the pod's network
with Sidekick, which needs to reach Slack, model providers and sources.
In this mode the validator itself keeps its tools off the network: unless
`allowDownloads` is set, OpenTofu may install providers from the mirror
only, and every tool gets an HTTP and HTTPS proxy on a local port that
refuses connections. This stops downloads, but it is not a network
boundary. If you need the validator kept off the network at the network
level, use Job mode.

With `networkPolicy.restrictEgress` and `allowDownloads` both on, add an
egress rule for port 443 to `networkPolicy.egress`, since the sidecar's
downloads leave from Sidekick's pod.

### Job mode

`validator.mode: job` runs each validation as a Kubernetes Job in the
release's namespace instead, for clusters that do not want a long-running
extra container, or that want the validator kept off the network at the
network level. Each validation takes a few seconds longer, to start its
pod.

The chart then makes:

- a Role and a RoleBinding that let Sidekick's service account create,
  read and delete Jobs and ConfigMaps (the request goes in a ConfigMap)
  and read pods and their logs, in the release's namespace only;
- a NetworkPolicy that gives the Job pods no ingress and no egress, or DNS
  and port 443 with `allowDownloads`, when `networkPolicy.enabled` is on.

Sidekick calls the Kubernetes API to create the Jobs, so in this mode
the Sidekick pods mount their service account token, whatever
`serviceAccount.automountToken` says. Creating Jobs lets Sidekick run pods
in its namespace, so install it in a namespace of its own.

### Keeping tokens away from the validator

The files the validator checks are written by the model, and OpenTofu
evaluates functions such as `file()` while it validates a module. A
module like `locals { x = tonumber(file("/var/run/secrets/kubernetes.io/serviceaccount/token")) }`
would put the file's contents into the error that goes back to Sidekick
and the model. So the validator's container must not see any token:

- **Kubernetes service account token.** The sidecar has an empty,
  read-only directory mounted at `/var/run/secrets/kubernetes.io/serviceaccount`.
  Kubernetes does not add its token mount to a container that already
  has a mount at that path, so the sidecar gets no token even when the
  Sidekick container does (with a Kubernetes source, or in job mode).
- **AWS (IRSA and EKS Pod Identity).** The pod gets the annotation
  `eks.amazonaws.com/skip-containers: validator`. The EKS pod identity
  webhook, which adds the IRSA token and the EKS Pod Identity token to
  containers, skips the containers it names, for both kinds of token.
- **Azure Workload Identity.** The pod gets the annotation
  `azure.workload.identity/skip-containers: validator`, so the Azure
  webhook adds no token volume to the validator container.

When `podAnnotations` already sets one of these annotations, the chart
adds `validator` to its list. The validator also replaces anything that
looks like a JWT in the messages it returns with `<redacted>`, as a second
line of defence.

In job mode, the Job pods must likewise get no token: they must run with
`automountServiceAccountToken: false`, without the
`azure.workload.identity/use` label or any cloud identity annotation, and
with a service account other than Sidekick's, since the EKS webhook adds
the IRSA token to pods whose service account names an IAM role.

## Limits

- A validation, all its files included, gets 60 seconds.
- At most 2 validations run at a time. A request waits up to 60 seconds
  for a free slot, then gets a busy error.
- A request holds at most 500 files and 8 MiB.
- Each validation's work directory is removed when it ends.

## Running it yourself

The image runs the server by default:

```sh
docker run --rm --read-only --tmpfs /tmp:exec -p 8200:8200 \
  ghcr.io/nourtalaat/sidekick-validator:<version> serve --listen 0.0.0.0:8200
```

`POST /validate` takes the format and the files, and answers with the
result:

```sh
curl -s localhost:8200/validate -d @- <<'EOF'
{"format": "prometheus_rules",
 "files": [{"path": "rules/checkout.yaml", "content": "groups: []\n"}]}
EOF
```

```json
{"ok": true, "errors": []}
```

A `terraform` request holds the module directory's files and may name
the module with `"dir"` when the files include other directories, such as
local modules it uses. Each error has a `path`, a `line` when the tool
gives one, and a `message`. The answer is 400 for a request that is
wrong, 503 when the validator stayed busy, and 504 when a validation took
longer than 60 seconds.

`sidekick-validate job --request <file>` runs one request from a file,
prints the result, and writes it to `/dev/termination-log`, cut down to
the 4096 bytes Kubernetes keeps there, with `"truncated": true` when
anything was left out. It exits with 0 when the validation ran, whether
the files passed or not, and with 1 when it could not run, after printing
`{"error": "..."}`.

## Verifying the image

The validator image is signed and has a signed SPDX SBOM, like the
Sidekick image:

```sh
cosign verify ghcr.io/nourtalaat/sidekick-validator:<version> \
  --certificate-identity https://github.com/nourtalaat/sidekick/.github/workflows/release.yml@refs/tags/v<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation ghcr.io/nourtalaat/sidekick-validator:<version> --type spdxjson \
  --certificate-identity https://github.com/nourtalaat/sidekick/.github/workflows/release.yml@refs/tags/v<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
