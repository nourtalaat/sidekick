{{/* The chart's name, which nameOverride replaces. */}}
{{- define "sidekick.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* The release's full name, the base of every resource name. */}}
{{- define "sidekick.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/* The labels every resource has. */}}
{{- define "sidekick.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "sidekick.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* The labels that select the release's pods. */}}
{{- define "sidekick.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sidekick.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* The service account the Sidekick pods run as. */}}
{{- define "sidekick.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sidekick.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* The Sidekick image, by digest when one is set. */}}
{{- define "sidekick.image" -}}
{{- if .Values.image.digest }}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}
{{- end }}

{{/* The bundled Postgres: its name, which is also its Service's and Secret's. */}}
{{- define "sidekick.postgres.fullname" -}}
{{- printf "%s-postgres" (include "sidekick.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* The bundled Postgres image. */}}
{{- define "sidekick.postgres.image" -}}
{{- printf "%s:%s" .Values.postgres.image.repository .Values.postgres.image.tag }}
{{- end }}

{{/*
The bundled Postgres password: the value, or the one in the existing
Secret, so an upgrade keeps it, or else a new random one.
*/}}
{{- define "sidekick.postgres.password" -}}
{{- if .Values.postgres.password }}
{{- .Values.postgres.password }}
{{- else }}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "sidekick.postgres.fullname" .) }}
{{- if and $existing $existing.data (index $existing.data "password") }}
{{- index $existing.data "password" | b64dec }}
{{- else }}
{{- randAlphaNum 32 }}
{{- end }}
{{- end }}
{{- end }}

{{/* Where Sidekick's secrets are mounted, the default secrets.dir. */}}
{{- define "sidekick.secretsDir" -}}
/var/run/secrets/sidekick
{{- end }}

{{/*
The Secrets the Sidekick pods mount: those in secrets, those externalSecrets
makes, and the bundled Postgres's. Each name appears once.
*/}}
{{- define "sidekick.secretNames" -}}
{{- $names := list }}
{{- range .Values.secrets }}
{{- $names = append $names . }}
{{- end }}
{{- range .Values.externalSecrets }}
{{- $names = append $names .name }}
{{- end }}
{{- if .Values.postgres.enabled }}
{{- $names = append $names (include "sidekick.postgres.fullname" .) }}
{{- end }}
{{- toJson ($names | uniq) }}
{{- end }}

{{/*
sidekick.yaml: .Values.config with the settings the chart owns. The
listeners match the container ports, secrets.dir matches the mounts, the
migration mode follows migrations.mode, and with the bundled Postgres the
database URL comes from its Secret unless config names another one.
*/}}
{{- define "sidekick.config" -}}
{{- $cfg := deepCopy (default dict .Values.config) }}
{{- $server := default dict (get $cfg "server") }}
{{- $_ := set $server "listen" (printf ":%d" (int .Values.service.httpPort)) }}
{{- $_ = set $server "metricsListen" (printf ":%d" (int .Values.service.metricsPort)) }}
{{- $_ = set $cfg "server" $server }}
{{- $_ = set $cfg "secrets" (dict "dir" (include "sidekick.secretsDir" .)) }}
{{- $database := default dict (get $cfg "database") }}
{{- $_ = set $database "migrations" (ternary "startup" "manual" (eq .Values.migrations.mode "startup")) }}
{{- if and .Values.postgres.enabled (not (get $database "urlSecret")) }}
{{- $_ = set $database "urlSecret" (dict "name" (include "sidekick.postgres.fullname" .) "key" "url") }}
{{- end }}
{{- $_ = set $cfg "database" $database }}
{{- toYaml $cfg }}
{{- end }}

{{/*
GOMEMLIMIT: 90% of the memory limit, which leaves room for memory the Go
garbage collector does not manage. Limits in Mi or Gi are converted here;
any other limit is passed whole through the downward API.
*/}}
{{- define "sidekick.goMemLimit" -}}
{{- $limit := dig "limits" "memory" "" (default dict .Values.resources) | toString }}
{{- if regexMatch "^[0-9]+Mi$" $limit }}
value: {{ printf "%dMiB" (div (mul (trimSuffix "Mi" $limit | atoi) 90) 100) | quote }}
{{- else if regexMatch "^[0-9]+Gi$" $limit }}
value: {{ printf "%dMiB" (div (mul (trimSuffix "Gi" $limit | atoi) 1024 90) 100) | quote }}
{{- else if $limit }}
valueFrom:
  resourceFieldRef:
    containerName: sidekick
    resource: limits.memory
{{- end }}
{{- end }}

{{/* The security context of every container: Sidekick's own. */}}
{{- define "sidekick.containerSecurityContext" -}}
{{- toYaml .Values.securityContext }}
{{- end }}

{{/* Checks of values that the schema cannot express. */}}
{{- define "sidekick.validate" -}}
{{- if and (eq .Values.migrations.mode "job") .Values.postgres.enabled }}
{{- fail "migrations.mode job needs an external database: the bundled Postgres starts after the pre-install migration Job, so use mode startup with postgres.enabled" }}
{{- end }}
{{- if and (eq .Values.migrations.mode "job") (not (dig "database" "urlSecret" "name" "" (default dict .Values.config))) }}
{{- fail "migrations.mode job needs config.database.urlSecret, the Secret the migration Job reads the database URL from" }}
{{- end }}
{{- if and (not .Values.postgres.enabled) (not (dig "database" "urlSecret" "name" "" (default dict .Values.config))) }}
{{- fail "set config.database.urlSecret to the Secret that holds the database URL, or set postgres.enabled for an evaluation install" }}
{{- end }}
{{- end }}
