{{/* The validator image, by digest when one is set. */}}
{{- define "sidekick.validator.image" -}}
{{- $image := .Values.validator.image }}
{{- if $image.digest }}
{{- printf "%s@%s" $image.repository $image.digest }}
{{- else }}
{{- printf "%s:%s" $image.repository (default .Chart.AppVersion $image.tag) }}
{{- end }}
{{- end }}

{{/* "true" when the validator runs as a sidecar of the Sidekick pods. */}}
{{- define "sidekick.validator.sidecar" -}}
{{- if and .Values.validator.enabled (eq .Values.validator.mode "sidecar") }}true{{ end }}
{{- end }}

{{/* "true" when the validator runs as Jobs that Sidekick creates. */}}
{{- define "sidekick.validator.job" -}}
{{- if and .Values.validator.enabled (eq .Values.validator.mode "job") }}true{{ end }}
{{- end }}

{{/* The labels of the validator's Job pods, which its NetworkPolicy selects. */}}
{{- define "sidekick.validator.selectorLabels" -}}
{{ include "sidekick.selectorLabels" . }}
app.kubernetes.io/component: validator
{{- end }}

{{/*
The validator sidecar container. It listens on loopback only, which the
kubelet's HTTP probes cannot reach, so the probes run
`sidekick-validate health` in the container. There is no readiness probe:
a validator that is down fails validations, and never takes Sidekick out
of service.
*/}}
{{- define "sidekick.validator.container" -}}
- name: validator
  image: {{ include "sidekick.validator.image" . }}
  imagePullPolicy: {{ .Values.validator.image.pullPolicy }}
  args:
    - serve
    - --work-dir
    - /tmp
    {{- if .Values.validator.allowDownloads }}
    - --allow-downloads
    {{- end }}
  startupProbe:
    exec: { command: [/usr/local/bin/sidekick-validate, health] }
    periodSeconds: 2
    timeoutSeconds: 3
    failureThreshold: 30
  livenessProbe:
    exec: { command: [/usr/local/bin/sidekick-validate, health] }
    periodSeconds: 30
    timeoutSeconds: 5
    failureThreshold: 3
  resources:
    {{- toYaml .Values.validator.resources | nindent 4 }}
  securityContext:
    {{- include "sidekick.containerSecurityContext" . | nindent 4 }}
  volumeMounts:
    - name: validator-tmp
      mountPath: /tmp
{{- end }}

{{/* The validator sidecar's work directory. */}}
{{- define "sidekick.validator.volume" -}}
- name: validator-tmp
  emptyDir:
    sizeLimit: {{ .Values.validator.workDirSizeLimit }}
{{- end }}
