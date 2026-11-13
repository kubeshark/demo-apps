{{- define "demo-apps.validateTLSFixture" -}}
{{- $fixture := .Values.tlsFixture -}}
{{- if $fixture.name -}}
{{- $names := list "go-stripped-traefik" "go-crypto-tls-bifrost" "boringssl-envoy" "openssl-mongodb" "openssl-mysql" "openssl-postgresql" "openssl-ingress-nginx" "openssl-nginx-ingress-f5" "boringssl-istio-gateway" "kfl-tls-capture" -}}
{{- if not (has $fixture.name $names) -}}
{{- fail "tlsFixture.name must select a supported TLS fixture" -}}
{{- end -}}
{{- if or (include "demo-apps.anyFlow" .) .Values.tls.enabled .Values.clients.java.enabled .Values.clients.python.enabled .Values.clients.node.enabled .Values.gateway.ingress.enabled .Values.gateway.externalURL .Values.runner.http2 -}}
{{- fail "tlsFixture requires all generic flows, TLS, clients, ingress, externalURL and runner.http2 disabled; use values-functional.yaml" -}}
{{- end -}}
{{- if eq $fixture.name "kfl-tls-capture" -}}
{{- if or $fixture.markerPrefix $fixture.markerCount $fixture.interval -}}
{{- fail "kfl-tls-capture uses its original opaque client cadence and does not accept markerPrefix, markerCount or interval" -}}
{{- end -}}
{{- else -}}
{{- $prefix := toString $fixture.markerPrefix -}}
{{- if not (or (eq $prefix "${MARKER_PREFIX}") (regexMatch "^[A-Za-z0-9][A-Za-z0-9_-]*$" $prefix)) -}}
{{- fail "tlsFixture.markerPrefix must be a nonempty alphanumeric, underscore or hyphen marker, or ${MARKER_PREFIX}" -}}
{{- end -}}
{{- $count := toString $fixture.markerCount -}}
{{- if not (or (eq $count "${EXPECTED_MARKERS}") (regexMatch "^[1-9][0-9]*$" $count)) -}}
{{- fail "tlsFixture.markerCount must be a positive integer or ${EXPECTED_MARKERS}" -}}
{{- end -}}
{{- if not (regexMatch "^[1-9][0-9]*s$" (toString $fixture.interval)) -}}
{{- fail "tlsFixture.interval must be a positive whole-second duration such as 2s or 3s" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "demo-apps.tlsFixtureSeconds" -}}
{{- trimSuffix "s" .Values.tlsFixture.interval -}}
{{- end -}}

{{- define "demo-apps.tlsFixtureClusterName" -}}
{{- printf "tls-%s-%s" (sha256sum .root.Release.Namespace | trunc 16) .suffix -}}
{{- end -}}
