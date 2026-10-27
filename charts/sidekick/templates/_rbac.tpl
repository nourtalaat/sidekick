{{/*
What the Kubernetes sources in config read in the cluster Sidekick runs
in, as JSON. Only sources of type kubernetes without kubeconfigSecret
count; one with a kubeconfigSecret reads another cluster. "any" says
whether there is such a source. "clusterWide" says whether one of them
reads every namespace, and "logs" whether one of them has logs on.
"namespaces" maps each namespace the others list to whether a source that
lists it has logs on.
*/}}
{{- define "sidekick.kubernetesSource.access" -}}
{{- $access := dict "any" false "clusterWide" false "logs" false "namespaces" dict }}
{{- range $name, $src := dig "sources" dict (default dict .Values.config) }}
{{- if and (kindIs "map" $src) (eq (toString (get $src "type")) "kubernetes") (not (get $src "kubeconfigSecret")) }}
{{- $logs := eq (toString (get $src "logs")) "true" }}
{{- $_ := set $access "any" true }}
{{- if $logs }}
{{- $_ = set $access "logs" true }}
{{- end }}
{{- $namespaces := default list (get $src "namespaces") | compact }}
{{- if not $namespaces }}
{{- $_ = set $access "clusterWide" true }}
{{- end }}
{{- range $namespaces }}
{{- $before := and (hasKey $access.namespaces .) (index $access.namespaces .) }}
{{- $_ = set $access.namespaces . (or $logs $before) }}
{{- end }}
{{- end }}
{{- end }}
{{- toJson $access }}
{{- end }}

{{/*
The name of the Kubernetes source's Roles, ClusterRoles and bindings. It
starts with the release's namespace, because a ClusterRole, and a Role in
a namespace that several releases read, must not share a name across
releases.
*/}}
{{- define "sidekick.kubernetesSource.rbacName" -}}
{{- printf "%s-%s-kubernetes-source" .Release.Namespace (include "sidekick.fullname" .) }}
{{- end }}

{{/*
The rules of the Kubernetes source: exactly the calls its tools make, and
nothing on Secrets or ConfigMaps. Takes a dict with "logs", which adds get
on pods/log for get_pod_logs, and "namespaced", which leaves out nodes.
*/}}
{{- define "sidekick.kubernetesSource.rules" -}}
# list_events and list_pod_problems.
- apiGroups: [""]
  resources: [events, pods]
  verbs: [list]
{{- if .logs }}
# get_pod_logs.
- apiGroups: [""]
  resources: [pods/log]
  verbs: [get]
{{- end }}
# get_workload and get_rollout_history.
- apiGroups: [apps]
  resources: [deployments, statefulsets, daemonsets]
  verbs: [get]
# get_rollout_history.
- apiGroups: [apps]
  resources: [replicasets]
  verbs: [list]
# get_workload.
- apiGroups: [autoscaling]
  resources: [horizontalpodautoscalers]
  verbs: [list]
{{- if not .namespaced }}
{{ include "sidekick.kubernetesSource.nodeRules" . }}
{{- end }}
{{- end }}

{{/* The rule of list_nodes. Nodes have no namespace. */}}
{{- define "sidekick.kubernetesSource.nodeRules" -}}
# list_nodes.
- apiGroups: [""]
  resources: [nodes]
  verbs: [get, list]
{{- end }}

{{/* The subject of the Kubernetes source's bindings: Sidekick's service account. */}}
{{- define "sidekick.kubernetesSource.subjects" -}}
- kind: ServiceAccount
  name: {{ include "sidekick.serviceAccountName" . }}
  namespace: {{ .Release.Namespace }}
{{- end }}

{{/*
Whether the Sidekick pods mount a service account token: when
serviceAccount.automountToken is set, when a Kubernetes source reads
the cluster with the pod's service account, or when Sidekick creates the
validator's Jobs.
*/}}
{{- define "sidekick.automountToken" -}}
{{- $access := include "sidekick.kubernetesSource.access" . | fromJson }}
{{- or .Values.serviceAccount.automountToken $access.any (eq (include "sidekick.validator.job" .) "true") }}
{{- end }}
