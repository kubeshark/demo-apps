#!/usr/bin/env bash
# Runs every enabled flow in every variant through the gateway API and fails
# on the first run the gateway reports as failed. Works against any cluster:
#   hack/smoke.sh <namespace> <release>
set -euo pipefail

ns=${1:-demo-apps}
release=${2:-demo-apps}
port=${SMOKE_PORT:-18080}
here=$(cd "$(dirname "$0")" && pwd)

kubectl -n "$ns" rollout status deploy -l app.kubernetes.io/part-of=demo-apps --timeout=300s

kubectl -n "$ns" port-forward "svc/${release}-gateway" "${port}:80" >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do
  curl -sf "http://127.0.0.1:${port}/healthz" >/dev/null && break
  sleep 1
done

python3 "$here/smoke.py" "http://127.0.0.1:${port}"

# Each TLS client must complete its https call through the gateway and its call to its
# server (postgresql or the inventory TLS listener) at least once.
for d in $(kubectl -n "$ns" get deploy -l app.kubernetes.io/part-of=demo-apps -o name | grep -- '-client-' || true); do
  ok=""
  for _ in $(seq 1 30); do
    logs=$(kubectl -n "$ns" logs "$d" --tail=50 2>/dev/null || true)
    if grep -q 'https flow=.* status=200' <<<"$logs" && grep -q -E 'postgresql selected=KSKEEP|grpc KSKEEP' <<<"$logs"; then
      ok=1
      break
    fi
    sleep 3
  done
  if [ -z "$ok" ]; then
    echo "FAIL $d:"; kubectl -n "$ns" logs "$d" --tail=20
    exit 1
  fi
  echo "ok   ${d#deployment.apps/}: https and server calls over tls"
done

echo "--- runner log"
kubectl -n "$ns" logs deploy/"${release}-runner" --tail=20
