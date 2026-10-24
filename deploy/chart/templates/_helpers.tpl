{{/*
Which services and servers each flow needs. Enabling a flow renders every component
listed here; a component is rendered once even when several flows need it.
*/}}
{{- define "demo-apps.needs" -}}
catalog: [catalog, redis, mysql]
inventory: [inventory, mongodb]
orders: [orders, notifier, postgresql, kafka, rabbitmq]
graphql: [graphql, postgresql]
websocket: [live]
identity: [identity, openldap, freeradius]
telco: [telco, telco-server]
netprobe: [netprobe, coredns]
ledger: [ledger, mssql]
{{- end }}

{{/* true when any enabled flow needs the component named in .component */}}
{{- define "demo-apps.need" -}}
{{- $needs := include "demo-apps.needs" . | fromYaml -}}
{{- $out := "" -}}
{{- range $flow, $cfg := .Values.flows -}}
  {{- if $cfg.enabled -}}
    {{- range $c := index $needs $flow -}}
      {{- if eq $c $.component -}}{{- $out = "true" -}}{{- end -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- $out -}}
{{- end }}

{{- define "demo-apps.anyFlow" -}}
{{- $out := "" -}}
{{- range $flow, $cfg := .Values.flows -}}{{- if $cfg.enabled -}}{{- $out = "true" -}}{{- end -}}{{- end -}}
{{- $out -}}
{{- end }}

{{- define "demo-apps.tag" -}}
{{- .Values.global.imageTag | default .Chart.AppVersion -}}
{{- end }}
{{/* image built in this repository: .root and .name */}}
{{- define "demo-apps.image" -}}
{{- $repo := .root.Values.global.registry -}}
{{- with .root.Values.global.imagePrefix }}{{ $repo = printf "%s/%s" $repo . }}{{ end -}}
{{- printf "%s/%s:%s" $repo .name (include "demo-apps.tag" .root) -}}
{{- end }}

{{/*
mirrored third-party image: .root and .image (name, tag, digest, optional repository).
repository, when set, replaces registry/prefix/name verbatim; the chart's ci values use it
to pull servers from their upstream registries.
*/}}
{{- define "demo-apps.mirrorImage" -}}
{{- $repo := .root.Values.global.mirrorRegistry | default .root.Values.global.registry -}}
{{- with .root.Values.global.mirrorPrefix }}{{ $repo = printf "%s/%s" $repo . }}{{ end -}}
{{- $ref := printf "%s/%s:%s" $repo .image.name .image.tag -}}
{{- with .image.repository }}{{ $ref = printf "%s:%s" . $.image.tag }}{{ end -}}
{{- if .image.digest }}{{ $ref }}@{{ .image.digest }}{{ else }}{{ $ref }}{{ end -}}
{{- end }}

{{- define "demo-apps.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/part-of: demo-apps
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end }}

{{- define "demo-apps.selector" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end }}

{{- define "demo-apps.securityContext" -}}
runAsNonRoot: true
runAsUser: 65532
allowPrivilegeEscalation: false
capabilities:
  drop: [ALL]
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{- define "demo-apps.secretName" -}}
{{- .server.existingSecret | default (printf "%s-%s" .root.Release.Name .name) -}}
{{- end }}
