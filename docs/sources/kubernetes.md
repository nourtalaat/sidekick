# The Kubernetes source

The Kubernetes source (`type: kubernetes`) lets an investigation read a
cluster: events, workload status, pods with problems, rollout history, node
conditions and, if you turn it on, pod logs. Every tool only reads, and the
source never reads Secrets or ConfigMaps. This page lists its keys, its
tools and the smallest set of permissions it needs.

## Config

```yaml
sources:
  k8s:
    type: kubernetes
    namespaces: [prod, staging]
    logs: true
    consoleURL: "https://headlamp.example.com/c/main/{{.Resource}}{{if .Namespace}}/{{.Namespace}}{{end}}/{{.Name}}"
```

| Key | Default | Meaning |
|---|---|---|
| `namespaces` | every namespace | The namespaces the tools may read. With a list, the model can only name these namespaces, and a tool given no namespace reads each of them. Without it, the source needs a ClusterRole. |
| `logs` | `false` | Turns on `get_pod_logs`, which needs `get` on `pods/log`. Logs can hold personal data and secrets that an application printed, so it is off unless you choose it. Logs are redacted like every tool result. |
| `consoleURL` | none | A Go template for evidence links to one object, for example in Headlamp or the Kubernetes dashboard. It gets `.Kind` (`Deployment`), `.Resource` (`deployments`), `.Namespace` (empty for nodes) and `.Name`, each escaped for a URL path. `sidekick config validate` renders it for a sample object and rejects a template that does not give an absolute `http` or `https` URL. |
| `kubeconfigSecret` | none | `{ name, key }` of a Secret that holds a kubeconfig file, to read a cluster other than the one Sidekick runs in. Without it, Sidekick uses its pod's service account. |
| `context` | the file's current context | The kubeconfig context to use. Only with `kubeconfigSecret`. |

The shared source keys (`tools.allow`, `tools.deny`, `tools.rename`,
`timeout` and `concurrency`) work as for every source; see
[`sources`](../configuration.md#sources).

## Tools

The model sees each tool as `<source>__<tool>`, for example
`k8s__list_events`.

| Tool | Returns |
|---|---|
| `list_events` | Events for a namespace, or for one object by kind and name, newest first. It can keep only `Warning` or `Normal` events, and only those since a time (`since` is an RFC 3339 time or a duration back from now, such as `2h`). Default 50 events, at most 500. |
| `get_workload` | The status of a Deployment, StatefulSet or DaemonSet: replicas, generation, revision, update strategy, conditions, container images, and the horizontal pod autoscaler that scales it, if any. |
| `list_pod_problems` | Pods with restarts, OOM kills, `CrashLoopBackOff`, image pull errors and other waiting reasons, pods that are pending or cannot be scheduled, failed or evicted pods, and running containers that are not ready. It can take a label selector. Pods that finished successfully are left out. Default 50 pods, at most 200, most restarts first. |
| `get_rollout_history` | The ReplicaSet revisions of a Deployment, newest first, with their creation times, images, change cause and what changed from the revision before. Default 10 revisions, at most 50. |
| `get_pod_logs` | The last lines of one container's logs, with timestamps: default 200 lines, at most 2,000, cut to the newest 256 KiB. It can read the container's previous run, for example the one before a crash. Only with `logs: true`. |
| `list_nodes` | Nodes with their conditions (ready, memory, disk and PID pressure), cordons, taints, roles, kubelet versions and allocatable CPU, memory and pods. It can keep only nodes with problems. Default 100 nodes, at most 500, problems first. |

How it behaves:

- **Empty results.** A tool that finds nothing returns no data, not an
  error, so the report counts the source as checked with no data.
- **Errors the model can act on.** An object that does not exist, a bad
  label selector or a namespace outside `namespaces` go back to the model
  as a tool error, so it can try something else. So does a request the
  cluster refuses. Its message names the verb, resource and API group
  that the Role or ClusterRole is missing, so you can fix the RBAC from
  the investigation's record.
- **Failures.** An API server that cannot be reached, or a kubeconfig that
  cannot be read, fails the call. After 5 failures in a row the source is
  skipped for 60 seconds, as for every source.
- **Evidence.** With `consoleURL`, each object a result names gets a link,
  at most 10 per result. Without it, or when a result names no single
  object, the evidence is the `kubectl` command that shows the same thing,
  for example `kubectl rollout history deployment/checkout -n prod`.
  Slack shows only evidence with a link; the command is kept in the
  investigation's record.
- **Credentials.** The service account token is read from its file, so a
  rotated token is used without a restart. A `kubeconfigSecret` is read on
  each call, and the client is built again only when the file changes. It
  may hold only inline credentials; see
  [Another cluster](#another-cluster-a-kubeconfig-secret).
- **Size.** Results over 256 KiB are cut and marked as cut.

## Permissions

The source only reads. Each tool makes single `get` or `list` calls and
keeps nothing open, so it needs no `watch`, and each resource gets only the
verb its tools use. It needs no access to Secrets or ConfigMaps, which
often hold credentials, so do not grant it any.

| API group | Resources | Verbs | Used by |
|---|---|---|---|
| core (`""`) | `events` | `list` | `list_events` |
| core (`""`) | `pods` | `list` | `list_pod_problems` |
| core (`""`) | `pods/log` | `get` | `get_pod_logs`, only with `logs: true` |
| core (`""`) | `nodes` (cluster scope) | `get`, `list` | `list_nodes` |
| `apps` | `deployments` | `get` | `get_workload`, `get_rollout_history` |
| `apps` | `statefulsets`, `daemonsets` | `get` | `get_workload` |
| `apps` | `replicasets` | `list` | `get_rollout_history` |
| `autoscaling` | `horizontalpodautoscalers` | `list` | `get_workload` |

A tool whose permission is missing fails with a tool error and the others
keep working, so you can leave out a tool you do not want, for example
`list_nodes`, and deny it with `tools.deny` so the model does not try it.

### With the Helm chart

The chart grants these rules itself, and mounts the pod's service account
token, for every source in `config.sources` with `type: kubernetes` and no
`kubeconfigSecret`. It reads `namespaces` and `logs` from those sources,
so the rules always match what the sources read:

- A source without `namespaces` gets one ClusterRole and
  ClusterRoleBinding with every rule.
- When every source lists `namespaces`, each listed namespace gets a Role
  and RoleBinding with the namespaced rules. `list_nodes` is always
  offered and nodes have no namespace, so a small ClusterRole allows `get`
  and `list` on nodes only.
- `get` on `pods/log` is in a Role only when a source that lists the
  namespace has `logs: true`, and in the ClusterRole when any source has
  it.
- Without such a source, the chart makes no roles and the pod mounts no
  token, unless `serviceAccount.automountToken` is set.

The roles are named after the release's namespace and full name, for
example `sidekick-sidekick-kubernetes-source` for the release `sidekick`
in the namespace `sidekick`, and the nodes ClusterRole has `-nodes` after
that. Each namespace a source
lists must exist before you install. To manage the roles yourself, for
example to leave out nodes, set `rbac.kubernetesSource: false` and apply
the rules below. The pod still mounts its token, because the source needs
it.

### Without the chart: every namespace

Without `namespaces`, grant the rules cluster-wide. Replace the service
account name and namespace with Sidekick's own.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sidekick-kubernetes-source
rules:
  - apiGroups: [""]
    resources: [events, pods]
    verbs: [list]
  # Only with logs: true.
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
  - apiGroups: [apps]
    resources: [deployments, statefulsets, daemonsets]
    verbs: [get]
  - apiGroups: [apps]
    resources: [replicasets]
    verbs: [list]
  - apiGroups: [autoscaling]
    resources: [horizontalpodautoscalers]
    verbs: [list]
  - apiGroups: [""]
    resources: [nodes]
    verbs: [get, list]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: sidekick-kubernetes-source
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: sidekick-kubernetes-source
subjects:
  - kind: ServiceAccount
    name: sidekick
    namespace: sidekick
```

### Without the chart: some namespaces

With `namespaces`, grant the namespaced rules with a Role and RoleBinding
in each listed namespace. Nodes have no namespace, so `list_nodes` still
needs a small ClusterRole; leave it out, and deny the tool, if Sidekick
should not see nodes.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: sidekick-kubernetes-source
  namespace: prod                     # one Role and RoleBinding per namespace
rules:
  - apiGroups: [""]
    resources: [events, pods]
    verbs: [list]
  # Only with logs: true.
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
  - apiGroups: [apps]
    resources: [deployments, statefulsets, daemonsets]
    verbs: [get]
  - apiGroups: [apps]
    resources: [replicasets]
    verbs: [list]
  - apiGroups: [autoscaling]
    resources: [horizontalpodautoscalers]
    verbs: [list]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: sidekick-kubernetes-source
  namespace: prod
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: sidekick-kubernetes-source
subjects:
  - kind: ServiceAccount
    name: sidekick
    namespace: sidekick
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sidekick-kubernetes-source-nodes
rules:
  - apiGroups: [""]
    resources: [nodes]
    verbs: [get, list]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: sidekick-kubernetes-source-nodes
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: sidekick-kubernetes-source-nodes
subjects:
  - kind: ServiceAccount
    name: sidekick
    namespace: sidekick
```

### Another cluster: a kubeconfig Secret

To read a cluster Sidekick does not run in, create a service account there
with the same rules, put a kubeconfig file for it in a Secret, and name it
in `kubeconfigSecret`.

The file must carry its credentials itself. Sidekick accepts a `token`,
`client-certificate-data` with `client-key-data`, or a `username` and
`password`, with the cluster's CA in `certificate-authority-data`. It
refuses a file that would make it run a program or read another file in
its pod, and the source's calls fail with a message that names the user or
cluster and the key to use instead:

- `exec` credential plugins, such as `aws eks get-token`, and legacy
  `auth-provider` plugins;
- paths to files: `tokenFile`, `client-certificate`, `client-key` and
  `certificate-authority`.

`insecure-skip-tls-verify: true` works, but anyone on the network path to
the cluster could then read and change Sidekick's requests, so Sidekick
logs a warning each time it builds a client from such a file.

```yaml
sources:
  staging:
    type: kubernetes
    kubeconfigSecret: { name: staging-cluster, key: kubeconfig }
    context: staging
```
