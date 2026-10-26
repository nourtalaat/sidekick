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
  each call, and the client is built again only when the file changes.
- **Size.** Results over 256 KiB are cut and marked as cut.

## Permissions

The source only reads, with `get` and `list`. It does not need `watch`,
because every tool makes single `get` or `list` calls and keeps nothing
open. It needs no access to Secrets or ConfigMaps, which often hold
credentials, so do not grant it any.

| API group | Resources | Verbs | Used by |
|---|---|---|---|
| core (`""`) | `events` | `get`, `list` | `list_events` |
| core (`""`) | `pods` | `get`, `list` | `list_pod_problems` |
| core (`""`) | `pods/log` | `get` | `get_pod_logs`, only with `logs: true` |
| core (`""`) | `nodes` (cluster scope) | `get`, `list` | `list_nodes` |
| `apps` | `deployments`, `statefulsets`, `daemonsets` | `get`, `list` | `get_workload`, `get_rollout_history` |
| `apps` | `replicasets` | `get`, `list` | `get_rollout_history` |
| `autoscaling` | `horizontalpodautoscalers` | `get`, `list` | `get_workload` |

A tool whose permission is missing fails with a tool error and the others
keep working, so you can leave out a tool you do not want, for example
`list_nodes`, and deny it with `tools.deny` so the model does not try it.

### Every namespace: one ClusterRole

Without `namespaces`, grant the rules cluster-wide. Replace the service
account name and namespace with Sidekick's own.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sidekick-kubernetes-source
rules:
  - apiGroups: [""]
    resources: [events, pods, nodes]
    verbs: [get, list]
  - apiGroups: [apps]
    resources: [deployments, replicasets, statefulsets, daemonsets]
    verbs: [get, list]
  - apiGroups: [autoscaling]
    resources: [horizontalpodautoscalers]
    verbs: [get, list]
  # Only with logs: true.
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
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

### Some namespaces: a Role in each

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
    verbs: [get, list]
  - apiGroups: [apps]
    resources: [deployments, replicasets, statefulsets, daemonsets]
    verbs: [get, list]
  - apiGroups: [autoscaling]
    resources: [horizontalpodautoscalers]
    verbs: [get, list]
  # Only with logs: true.
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
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
in `kubeconfigSecret`. Use a token or client certificate written into the
file; a kubeconfig that runs a command for credentials (`exec`) needs that
command in the Sidekick image, which the official image does not have.

```yaml
sources:
  staging:
    type: kubernetes
    kubeconfigSecret: { name: staging-cluster, key: kubeconfig }
    context: staging
```
