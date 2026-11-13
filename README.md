# demo-apps

Workloads that exercise every protocol dissector Kubeshark ships, grouped into flows a presenter
can trigger from a small UI and that also run on a timer. One real server per protocol, one Go
service per flow group talking to it through the protocol's real client library, and a declared
payload per flow so what was captured can be asserted, not only watched.

Images are published to ECR Public and the Helm chart as an OCI artifact, so the set installs on
any cluster without credentials.

## Install

```bash
helm install demo-apps oci://public.ecr.aws/l3y1d4r0/kubeshark/charts/demo-apps -n demo-apps --create-namespace
kubectl -n demo-apps port-forward svc/demo-apps-gateway 8080:80
```

Open http://localhost:8080, press Run on a flow, and follow the hops in Kubeshark. Each flow also
runs on its own interval (`flows.<name>.interval`), with a slow and an error variant on a fixed
cadence (`runner.slowEvery`, `runner.errorEvery`).

Kubeshark needs `deploy/kubeshark-values.yaml` to show every flow: it enables the `sctp` dissector
and keeps dissection on when nobody is watching the UI.

Cluster requirements: the telco flow needs SCTP through the CNI (on Cilium set `enable-sctp: "true"`
in `cilium-config` and restart the agents; without it the TCP Diameter leg still works), and the
netprobe pod needs the `NET_RAW` capability for ICMP. The ledger flow (MSSQL, 2 GiB) is off by
default.

## Functional-test reuse

`deploy/chart/values-functional.yaml` is included in every chart archive. It reuses the same
services, databases and protocol clients as the demo, but disables every flow, TLS, all language
clients, ingress and external egress. No workloads or Services render until a flow is enabled.
Enabled flows run every second with `runner.errorEvery=0` and `runner.slowEvery=0`.

Use a disposable namespace per case and the fixed release name `demo-apps` so resource names
remain `demo-apps-gateway`, `demo-apps-catalog`, etc. For local chart development:

```bash
helm install demo-apps deploy/chart -n functional-catalog --create-namespace \
  -f deploy/chart/values-functional.yaml --set flows.catalog.enabled=true \
  --set global.imageTag=<published-branch-sha7>
```

Enable `flows.inventory.enabled=true` for gRPC/MongoDB, or select any other existing flow.
Each selection renders its full dependency set; orders includes Kafka, PostgreSQL, notifier and
RabbitMQ. Override `flows.<name>.interval` to change the cadence.

Set `runner.http2=true` to make ordinary runner-to-gateway POST requests use cleartext HTTP/2
prior knowledge, independently of the inventory flow's gRPC traffic. The chart supplies
`GATEWAY_HTTP2=true`; the runner uses a dedicated transport and leaves external HTTPS traffic
unchanged. The default remains HTTP/1.1. The gateway accepts both on its plaintext listener.
Gateway-to-catalog requests remain HTTP/1.1 `GET /products/<sku>`, so GET-field assertions can
observe that hop without changing the gateway API's POST semantics.

For repeatable OCI runs, pin the exact dev chart version and record the downloaded `.tgz` SHA256
(the archive checksum, not the OCI manifest digest). Replace `<sha7>` with the published commit:

```bash
VERSION='0.2.1-functional-workloads.g<sha7>'
CHART=oci://public.ecr.aws/l3y1d4r0/kubeshark/charts/demo-apps
tmp=$(mktemp -d)
helm pull "$CHART" --version "$VERSION" --destination "$tmp"
sha256sum "$tmp/demo-apps-$VERSION.tgz"
tar -xzf "$tmp/demo-apps-$VERSION.tgz" -C "$tmp"
helm install demo-apps "$tmp/demo-apps-$VERSION.tgz" \
  -n functional-catalog --create-namespace \
  -f "$tmp/demo-apps/values-functional.yaml" --set flows.catalog.enabled=true
```

Helm does not automatically apply the bundled preset: pass it with `-f` after extracting it,
or use the local copy from the matching revision. Leave `global.imageTag` unset for an OCI chart;
its `appVersion` pins the matching published images. Delete the case namespace after the run.

### Specialized TLS fixtures

The chart also packages the original specialized TLS workloads used by perfshark. These are
separate from the generic flows above: select exactly one `tlsFixture.name` with the functional
preset, in a fresh namespace. Selection rejects enabled generic flows, generic TLS, language
clients, ingress, external gateway traffic and `runner.http2`; it never adds the demo gateway.

```bash
helm install demo-apps deploy/chart -n tls-traefik --create-namespace \
  -f deploy/chart/values-functional.yaml \
  --set tlsFixture.name=go-stripped-traefik \
  --set-string tlsFixture.markerPrefix=tls_demo \
  --set tlsFixture.markerCount=3 --set tlsFixture.interval=2s
```

| Fixture | Original binary or image | Original batch interval |
|---|---|---|
| `go-stripped-traefik` | Traefik 3.6.13 | `2s` |
| `go-crypto-tls-bifrost` | Bifrost 1.4.22-debug to pinned LiteLLM | `2s` |
| `boringssl-envoy` | Envoy 1.31.10 | `2s` |
| `openssl-mongodb` | MongoDB 8.0.26 | `3s` |
| `openssl-mysql` | MySQL 8.0 | `3s` |
| `openssl-postgresql` | PostgreSQL 16 | `3s` |
| `openssl-ingress-nginx` | ingress-nginx 1.15.1 | `2s` |
| `openssl-nginx-ingress-f5` | F5 NGINX Ingress Controller 5.4.2 | `2s` |
| `boringssl-istio-gateway` | Envoy from Istio proxyv2 1.25.1 | `2s` |
| `kfl-tls-capture` | Original mizutest OpenSSL Python client | Opaque image behavior |

For the nine marker fixtures, supply a nonempty prefix consisting of ASCII letters, digits,
underscores or hyphens (starting with a letter or digit), a positive integer `markerCount`, and a
positive whole-second `interval`. Each batch repeats `<prefix>_seq1` through `<prefix>_seq<count>`;
only the batch delay changes, not startup retries, request timeouts, clients or TLS hops.
For `kfl-tls-capture`, set only the name and leave marker values and interval at their defaults.
Its image, command and traffic behavior are not replaced by a marker generator.

Perfshark prepares manifests before generating an attempt-specific prefix. It can render the
exact literal strings `${MARKER_PREFIX}` and `${EXPECTED_MARKERS}` as `markerPrefix` and
`markerCount`, respectively. They are accepted only as these exact sentinels. The runner binds
only the corresponding environment entries on a fresh object copy before creating traffic;
runtime shell and ConfigMap scripts are not interpolated by a second renderer. Ordinary Helm
installs must use explicit values instead of these preparation sentinels.

Each fixture preserves its original Service names, so do not share a namespace between
fixtures or releases. The two ingress fixtures each create exactly one ClusterRole,
ClusterRoleBinding and IngressClass named `tls-<namespace-hash>-nginx` or
`tls-<namespace-hash>-f5`, where the hash is the first 16 hex characters of SHA256(namespace).
Controllers watch only that namespace and only their selected class; classes are not defaults.
Cluster permissions cover only nodes, namespaces and ingress classes. Namespaced permissions
stay in the original Role. F5 retains its required `nginx.org/ingress-controller` identity and
uses the unique class name, as described in its
[controller flag documentation](https://docs.nginx.com/nginx-ingress-controller/configuration/global-configuration/command-line-arguments/).
The original HTTPS Service hop, TLS hosts and F5 SNI/`--connect-to` pairing are preserved.

With Helm, uninstall the release before deleting its namespace to remove cluster-scoped objects.
Perfshark instead creates these objects with the live namespace UID as owner, rejects foreign
collisions and deletes only the UIDs it created. ingress-nginx keeps its `/wait-shutdown` preStop
and 300-second termination grace; cleanup must allow that grace plus a safety margin.

Observe traffic with a stream subscription active before workloads start. TLS decryption
assertions require the `tls` capture predicate together with the original hop filters and marker
content; a plaintext backend HTTP request alone is not proof of TLS decryption. MongoDB 8 retains
the functional suite's kernel compatibility skip rather than substituting the generic MongoDB 7
server. Fixture images are pinned by digest and are independent of `global.imageTag` and the
generic server mirror values; the retained version tags identify the original binaries.

## TLS

The gateway, the inventory gRPC API and postgresql also serve TLS from a CA the chart generates
(`tls.existingSecret` to bring your own). Three clients in other languages use it on
`clients.interval`: Java (JDK HttpClient, pgjdbc), Python (requests, psycopg on libpq) and Node
(`node:https`, @grpc/grpc-js). What Kubeshark decrypts depends on the TLS library each side uses;
observed with the tracer enabled:

| Client | HTTPS to the gateway | TLS to its server |
|---|---|---|
| Python | decrypted in the client (system OpenSSL) | postgresql: decrypted in the client |
| Java | not decrypted (JSSE has no hook) | postgresql: decrypted in the postgres server (OpenSSL) |
| Node | not decrypted (OpenSSL linked into the node binary) | inventory gRPC: not decrypted |

The Go binaries are built unstripped, because the tracer cannot hook stripped binaries of Go
versions missing from its offset table.

## Develop

```bash
make                    # build, test, lint, chart lint, resource budget
make check-generated    # proto codegen and chart marker copies are committed; this proves they match
```

Go images are built by [ko](https://ko.build), one per `cmd/<service>`; the Java, Python and Node
clients by docker from `clients/<lang>/Dockerfile`:

```bash
export KO_DOCKER_REPO=<registry>/kubeshark/demo-apps
make images TAG=dev-1
make client-images REGISTRY=<registry>/kubeshark/demo-apps TAG=dev-1   # then docker push them
helm upgrade --install demo-apps deploy/chart -n demo-apps --create-namespace \
  --set global.registry=<registry> --set global.imageTag=dev-1
hack/smoke.sh demo-apps demo-apps   # every flow in every variant, and every TLS client
```

To check what Kubeshark captured, port-forward the hub and the gateway and run the marker checker.
It runs every enabled flow in every variant (6 rounds by default) while streaming from the hub, and
fails if a declared marker never reaches a streamed entry; markers declared `wire` are reported,
not failed:

```bash
kubectl -n default port-forward svc/kubeshark-hub 18899:80 &
kubectl -n demo-apps port-forward svc/demo-apps-gateway 18080:80 &
go run ./tools/ksxcheck -hub http://127.0.0.1:18899 -gateway http://127.0.0.1:18080
```

To pull the third-party servers from their upstream registries instead of the mirror, add
`-f deploy/chart/ci/upstream-values.yaml`.

## Publishing

`terraform/` creates the ECR Public repositories (derived from `cmd/`, `clients/` and the chart's
server images) and the `demo-apps-github-actions` role. Apply it with credentials for the account
that owns the registry alias, then set the repository variable `AWS_ROLE_ARN` to its `role_arn`
output:

```bash
cd terraform && terraform init && terraform plan && terraform apply
gh variable set AWS_ROLE_ARN --body "$(terraform output -raw role_arn)"
```

From then on every branch push publishes images tagged `<sanitized-branch>-<sha7>` and, only
after both Go and language-client images succeed, an OCI chart version
`0.2.1-<sanitized-branch>.g<sha7>` with the image tag as `appVersion`. Branch names are lowercased,
characters outside `a-z`, `0-9` and `-` become `-`, repeated hyphens collapse, and the result is
truncated to 100 characters and stripped of edge hyphens. An empty name becomes `dev`; a purely
numeric name gets a `branch-` prefix. Thus `functional-workloads` publishes
`0.2.1-functional-workloads.g<sha7>` with `appVersion: functional-workloads-<sha7>`.
The `g` keeps the prerelease identifier valid even when the SHA prefix is all digits with a leading
zero. Use the exact version: prereleases are not selected by Helm's default stable-version lookup.

A `vX.Y.Z` tag still publishes images tagged `vX.Y.Z` and a chart version `X.Y.Z` with
`appVersion: vX.Y.Z`. The Mirror workflow keeps `kubeshark/mirror/*` in step with the chart's
pinned server digests (run it once by hand after the first apply).

Layout, invariants and how to add a flow: `AGENTS.md`.
