{{- define "demo-apps.validateFixture" -}}
{{- $fixture := .Values.fixture -}}
{{- if $fixture.name -}}
{{- $plain := list "http" "http-redis" "http-redis-kafka" "http-markers" "resilience-traffic" "targeting-pair" -}}
{{- $tls := list "go-stripped-traefik" "go-crypto-tls-bifrost" "boringssl-envoy" "openssl-mongodb" "openssl-mysql" "openssl-postgresql" "openssl-ingress-nginx" "openssl-nginx-ingress-f5" "boringssl-istio-gateway" -}}
{{- if not (has $fixture.name (concat $plain $tls (list "kfl-tls-capture"))) -}}
{{- fail "fixture.name must select a supported fixture" -}}
{{- end -}}
{{- if or (include "demo-apps.anyFlow" .) .Values.tls.enabled .Values.clients.java.enabled .Values.clients.python.enabled .Values.clients.node.enabled .Values.gateway.ingress.enabled .Values.gateway.externalURL .Values.runner.http2 -}}
{{- fail "fixture requires all generic flows, TLS, clients, ingress, externalURL and runner.http2 disabled; use values-functional.yaml" -}}
{{- end -}}
{{- $markers := or (has $fixture.name $tls) (has $fixture.name (list "http-markers" "resilience-traffic")) -}}
{{- $counted := or (has $fixture.name $tls) (eq $fixture.name "http-markers") -}}
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
{{- if has $fixture.name $tls -}}
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
