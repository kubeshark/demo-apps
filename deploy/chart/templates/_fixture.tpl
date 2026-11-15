{{- define "demo-apps.validateFixture" -}}
{{- $fixture := .Values.fixture -}}
{{- if $fixture.name -}}
{{- $contract := .Files.Get "files/contracts/workload.yaml" | fromYaml -}}
{{- if not (hasKey $contract.functional.fixtures $fixture.name) -}}
{{- fail "fixture.name must select a supported fixture" -}}
{{- end -}}
{{- $capabilities := index $contract.functional.fixtures $fixture.name -}}
{{- if or (include "demo-apps.anyFlow" .) .Values.tls.enabled .Values.clients.java.enabled .Values.clients.python.enabled .Values.clients.node.enabled .Values.gateway.ingress.enabled .Values.gateway.externalURL .Values.runner.http2 -}}
{{- fail "fixture requires all generic flows, TLS, clients, ingress, externalURL and runner.http2 disabled; use values-functional.yaml" -}}
{{- end -}}
{{- $markers := $capabilities.markerPrefix -}}
{{- $counted := $capabilities.markerCount -}}
{{- if $markers -}}
{{- $prefix := toString $fixture.markerPrefix -}}
{{- if not (or (eq $prefix "${MARKER_PREFIX}") (regexMatch "^[A-Za-z0-9][A-Za-z0-9_-]*$" $prefix)) -}}
{{- fail "fixture.markerPrefix must be a nonempty alphanumeric, underscore or hyphen marker, or ${MARKER_PREFIX}" -}}
{{- end -}}
{{- else if $fixture.markerPrefix -}}
{{- fail "this fixture does not accept markerPrefix" -}}
{{- end -}}
{{- if $counted -}}
{{- $count := toString $fixture.markerCount -}}
{{- if not (or (eq $count "${EXPECTED_MARKERS}") (regexMatch "^[1-9][0-9]*$" $count)) -}}
{{- fail "fixture.markerCount must be a positive integer or ${EXPECTED_MARKERS}" -}}
{{- end -}}
{{- else if $fixture.markerCount -}}
{{- fail "this fixture does not accept markerCount" -}}
{{- end -}}
{{- if $capabilities.interval -}}
{{- if not (regexMatch "^[1-9][0-9]*s$" (toString $fixture.interval)) -}}
{{- fail "fixture.interval must be a positive whole-second duration such as 2s or 3s" -}}
{{- end -}}
{{- else if $fixture.interval -}}
{{- fail "this fixture uses its original cadence and does not accept interval" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "demo-apps.fixtureSeconds" -}}
{{- trimSuffix "s" .Values.fixture.interval -}}
{{- end -}}

{{- define "demo-apps.fixtureClusterName" -}}
{{- printf "tls-%s-%s" (sha256sum .root.Release.Namespace | trunc 16) .suffix -}}
{{- end -}}

{{- define "demo-apps.trafficReadiness" -}}
readinessProbe:
  exec:
    command: ["test", "-f", "/tmp/demo-apps-ready"]
  periodSeconds: 2
{{- end -}}
