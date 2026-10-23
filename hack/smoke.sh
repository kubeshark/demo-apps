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

echo "--- runner log"
kubectl -n "$ns" logs deploy/"${release}-runner" --tail=20
