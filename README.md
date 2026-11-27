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

Kubeshark needs `deploy/chart/files/contracts/kubeshark-values.yaml` to show every flow: it
enables the `sctp` dissector and keeps dissection on when nobody is watching the UI.

Cluster requirements: the telco flow needs SCTP through the CNI (on Cilium set `enable-sctp: "true"`
in `cilium-config` and restart the agents; without it the TCP Diameter leg still works), and the
netprobe pod needs the `NET_RAW` capability for ICMP. The ledger flow (MSSQL, 2 GiB) is off by
default.

## Sensitive-data redaction

The wallet flow (`gateway -> wallet (http) -> redis`) carries the values Kubeshark's
sensitive-data redaction removes, each next to a look-alike that must survive. One ok run signs a
user in, reads the profile and pays twice:

| Request | Sensitive | Must survive |
|---|---|---|
| `POST /session` | json `password`; response `Set-Cookie` value, `X-Auth-Token`, json `access_token` and `refresh_token`; redis `AUTH wallet <password>` | user id, cookie `Path`, `X-Trace-Id` |
| `GET /profile?token=...&view=...` | `Authorization`, `X-API-Key`, `X-Auth-Token`, both `Cookie` values, a JWT in the unlisted `X-Client-Assertion`, query `token`; response json `ssn` | query `view`, json `display_name`, `pinned`, `tokenizer` |
| `POST /payments` | `Proxy-Authorization`; json `card_number`, `cvv`, `pin`, `apiKey`, `x-api-key`, `passwd`, `secret`, `client_secret`, `private_key`, a nested `password`, a `token` key written as `\u0074oken`; card numbers under unlisted keys, masked to their last four digits | json `description`, a UUID request id, a Luhn-valid number outside every issuer range, an `eyJ` token that is not a JWT, response `receipt` |
| `POST /payments/form` | form `password`, `card_number` | form `description` |

The slow variant uploads a 1 MiB and a 3 MiB json statement, either side of the default 2Mi scan
budget (`tap.redaction.maxScanSize`): the first is scanned, the second is replaced whole. The error
variant calls the profile with a revoked API key and gets a 401. The identity flow's passwords
(http login body and LDAP bind) and the websocket flow's subscribe `token` are redacted as well.

To reproduce, install Kubeshark with the packaged values, open the demo UI and press Run on
wallet, then look at the wallet requests in Kubeshark: every value above is in clear. Turn
real-time redaction on and run it again:

```bash
helm upgrade --install kubeshark kubeshark/kubeshark -n default \
  -f deploy/chart/files/contracts/kubeshark-values.yaml --set tap.redaction.realtime.enabled=true
```

The switch is a Helm value only, applied when the workers restart, and needs a Kubeshark release
that has `tap.redaction`. The same requests now show `[REDACTED]` in each sensitive value,
`************8298` and `**** **** **** 8298` for the card numbers, and every value in the "Must
survive" column unchanged. `tap.redaction.snapshot.enabled` does the same for snapshot
dissections.

Not redacted by design, so still visible with redaction on: per-flow and raw packet captures,
binary bodies, redis values other than `AUTH` arguments, and the LDAP add or modify that seeds the
identity passwords when the identity service starts. Urlencoded form bodies and the far end of the
statements are redacted but not carried in a streamed entry, so no stream checker can see them in
either state.

To assert all of it, run `tools/ksxcheck` (see Develop) once with redaction off and once with
`-redaction` after turning it on. Run both: a value reported absent only proves something once
the same value was seen without redaction.

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
VERSION='0.3.1-functional-workloads.g<sha7>'
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

### Packaged workload contract

Every chart archive contains `files/contracts/workload.yaml` with `schemaVersion: 1`.
Paths in this metadata are relative to the unpacked chart. Available generic flows are the keys
of `flows` in `values.yaml`, not a separate catalog. `functional.fixtures` declares supported
fixed fixtures and their accepted marker/interval settings; Helm validates against this same map.
`functional.httpWarmupFixture` selects the existing `http` fixture.

`corpus` binds the eight measured flows, their `1s` runner interval and `1s` language-client
interval to the unchanged absolute floors in `files/contracts/corpus-expected.yaml`. That file's
`window: 5m` is the required capture window. Every protocol key other than `window` is required
coverage. Use chart defaults from the matching release for other workload settings, including TLS,
language clients, the pinned Kafka versions and the runner's error/slow schedule. A different
window, flow set, cadence or workload content needs a newly measured contract, not scaled floors.
The packaged `files/contracts/kubeshark-values.yaml` is the authoritative dissector configuration.
SCTP transport remains required; unsupported L7/runtime coverage is explicit in the metadata.

The runner's `/ready` endpoint on port 8081 returns success only after every configured flow has
returned HTTP 200 from its existing loop. New or interval-changed flows must succeed again;
invalid or empty configuration is not ready. Shell fixture clients become ready after an existing
HTTP request, Redis operation sequence, Kafka delivery batch or database marker batch succeeds.
Language clients require both existing operations
to succeed in one iteration. These signals latch until container restart (or runner loop change).
Probes only read runtime state or `/tmp/demo-apps-ready`; they generate no workload requests.
Readiness proves traffic operation success, not capture activation, dissection or coverage.

The `http-markers` and `resilience-traffic` nginx paths intentionally return 404.
Their readiness requires a completed HTTP exchange, not a 2xx response.
Transport failures still leave the client unready.

The fixed Redis client retries its existing SET/GET sequence every three seconds, including
after failure. The fixed Kafka client keeps its consumer and existing admin/three-message
producer sequence, sleeping five seconds per attempt; readiness requires successful delivery
callbacks for all three messages. A mounted wrapper observes the original image-owned TLS
client's existing requests and marks readiness only for a 2xx response. These wrappers retain
the original images and workload content and do not copy image-owned credentials.

### Synthetic snapshot seed

The chart also owns `files/seeds/snapshot.pcap`. `snapshotSeed` in the packaged workload
contract records its chart-relative `path`, raw-byte `sha256` and `format: pcap-dlt-raw`.
The raw seed digest is separate from the chart archive checksum. Snapshot consumers must
resolve this metadata from the same pinned chart archive they record for workload identity.

This is newly generated synthetic data, not a captured or sanitized workload. It contains 25
complete IPv4/TCP HTTP/1.1 conversations, with handshakes, ordered segmented request/response
payloads, valid checksums and teardown. Only RFC 5737 documentation IPv4 addresses and
`snapshot.demo.invalid` occur as network identities. GET, POST, PUT and DELETE operations,
success/error statuses and synthetic JSON item counts vary; there are no credentials or
external inputs. Fixed timestamps and pure deterministic construction make regeneration exact:

```bash
python3 hack/snapshot_seed.py
```

The generator uses only Python's standard library and prints the raw digest and size. Commit
the generator, binary seed and matching metadata digest together. `make check-generated` checks
regeneration drift. `hack/chartcontract` checks complete PCAP/TCP/HTTP framing, synthetic-only
payload constraints and the actual packaged bytes; the seed must stay at or below 512 KiB.

demo-apps owns this seed and contract; perfshark consumes the packaged artifact rather than
maintaining a second copy. This intentionally changes snapshot content and identity, so snapshot
measurements are not interchangeable with earlier seed baselines. Preserve those baselines and
record the new seed digest for new measurements; changing the seed does not promote a baseline.
Functional/corpus definitions, measured floors, cadence and transport requirements are unchanged.
Consumers need a published, pinned chart containing this metadata before cutover. Local generation
does not publish images/charts or authorize live runs, stable releases or baseline promotion.


### Fixed functional fixtures

The chart packages the original plain and specialized TLS workloads used by perfshark. These are
separate from the generic flows above: select exactly one `fixture.name` with the functional
preset, in a fresh namespace. Selection rejects enabled generic flows, generic TLS, language
clients, ingress, external gateway traffic and `runner.http2`. Only `wallet-capture` adds its
own controlled gateway; no fixture enables the generic runner.

```bash
helm install demo-apps deploy/chart -n tls-traefik --create-namespace \
  -f deploy/chart/values-functional.yaml \
  --set fixture.name=go-stripped-traefik \
  --set-string fixture.markerPrefix=tls_demo \
  --set fixture.markerCount=3 --set fixture.interval=2s
```

The six plain profiles preserve the original topology, clients and cadence:

| Fixture | Workloads | Marker settings |
|---|---|---|
| `http` | curl to nginx every second | None |
| `http-redis` | HTTP plus Redis and its original Go client | None |
| `http-redis-kafka` | HTTP and Redis plus ZooKeeper, Kafka and its original Python client | None |
| `http-markers` | nginx plus finite `seq1..count` cycles, sleeping one second per cycle | `markerPrefix`, positive `markerCount` |
| `resilience-traffic` | nginx plus strictly increasing markers, sleeping 0.2 seconds per request | `markerPrefix` only |
| `targeting-pair` | Separate `wire-targeted` and `wire-untargeted` HTTP pairs, every second | None |

All plain profiles reject interval overrides. Non-marker profiles reject marker settings;
`resilience-traffic` rejects a marker count and never emits `EXPECTED_MARKERS`. Its required
same-node client/server affinity is preserved. HTTP, Redis and Kafka templates are shared by
the three fixed composition profiles, not exposed as an arbitrary composition API.
The legacy Kafka profile retains ZooKeeper's NodePort Service, the headless Kafka Service,
original broker settings and opaque client commands; it does not use the generic Kafka flow.
The six profiles use digest-pinned original private ECR images under
`851725373216.dkr.ecr.us-east-1.amazonaws.com`. Nodes must have permission and credentials
to pull these repositories; the chart does not provision registry credentials or substitute
public images. The original `latest` tags on opaque clients remain descriptive, with the
digest fixing the actual image.

#### Controlled wallet capture

`wallet-capture` packages a finite HTTP controller, the real wallet and gateway at
digest-pinned `v0.3.0`, and digest-pinned Redis. It reuses digest-pinned `client-python:v0.3.0` for the packaged standard-library
script; no application image rebuild is needed. It rejects intervals, marker settings and
application image tags other than `v0.3.0`. Generic flows are disabled, so the generic runner
is not rendered. Application images remain `v0.3.0` even in a branch-development chart.

Install the digest-verified chart archive with its packaged `values-functional.yaml` and
`--set fixture.name=wallet-capture`. The client pod (`app: http-capture-client`) contains
the gateway and controller; the wallet pod (`app: http-capture-wallet`) also contains Redis.
Both require pod anti-affinity against the other on `kubernetes.io/hostname`. At least two
schedulable Linux nodes are required; a one-node install stays Pending rather than silently
weakening the test. The verifier must also check actual node placement.

The controller Service `http-capture-client:8081` accepts:

```text
GET  /readyz
POST /run?mode=wallet&run=<unique-nonce>
POST /run?mode=burst&run=<unique-nonce>
```

Readiness fails closed unless wallet and gateway `/healthz` both return HTTP 200 over
dedicated connections. Only health requests run before a trigger, and those connections
are not reused for workload traffic. No autonomous wallet runner exists. The gateway
port is not exposed through a Service. Start capture subscriptions before triggering.

Each control POST blocks until every response is validated. `wallet` invokes the real
gateway `/api/flows/wallet/run?variant=slow`, then `variant=ok`, without sleeps or retries:
1 MiB and 3 MiB statement uploads, sign-in, profile, card payment, then form payment.
Its ledger requires the exact successful two-plus-four gateway step results and byte
acceptance details. It does not invent ordinary request sizes or app response bodies that
the existing gateway does not expose.

`burst` sends an initial authenticated profile, then one 1 MiB and six 3 MiB statement
uploads, each immediately followed by a profile: 15 exchanges, 19 MiB uploaded, one HTTP/1.1
socket, no sleeps, rate limit or retries. A closed/replaced connection fails the run.
Bodies and hashes are allocated before connecting and match `wallet.Statement` byte for
byte. The JSON password is at the tail, beyond the inline preview. Every request carries
`X-Perfshark-Run` and zero-based `X-Perfshark-Sequence`; profiles use the real demo API key
and session cookie and must return the exact demo profile, not a 401 or arbitrary 200.

Successful responses follow `files/contracts/wallet-capture.schema.json`: timestamps in
Unix nanoseconds, ordered exchanges, exact upload sizes, SHA256 and accepted bytes, plus
actual response bodies for `burst`. Ordinary wallet POST sizes are omitted, not reported
as zero. One run may be active at a time (409 otherwise); nonces are 1-128 ASCII letters,
digits, underscores or hyphens, starting with a letter or digit. A nonce is consumed even
on failure and cannot be replayed during that controller process. Any app/connection
validation failure returns 502 with an error and partial ledger, never successful capture
evidence. This unauthenticated control interface and synthetic credentials are for isolated
test namespaces only. The controller ledger proves application acceptance, not capture:
Perfshark must independently verify both node views, connection continuity and full stored
payloads, including redaction beyond previews.

Scoped provider validation (Helm, Python 3 and the repository Go toolchain required):

```bash
go test ./hack/chartcontract -run '^TestWalletCaptureFixture$' -count=1 -timeout=90s
```

This packages and renders the chart, validates placement/images/readiness/invalid settings,
runs isolated deterministic controller peers, and compares Python statement hashes to the
real Go `wallet.Statement`. The tests reject wrong order/size/credentials, false gateway
success, retry, reconnect and pacing. They do not replace live capture verification.


The ten TLS profiles retain their original settings:

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

For the nine TLS marker fixtures, supply a nonempty prefix consisting of ASCII letters, digits,
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

Every fixture object, including cluster-scoped ingress resources, carries
`demo-apps.kubeshark.co/fixture` equal to the selected profile. Resource names and selectors
remain unchanged; the identity label does not change which pods a Service or workload selects.

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

With Kubeshark's sensitive-data redaction on, add `-redaction`: `redact` markers must then be
absent and `masked` markers present. Run it both ways, as described under "Sensitive-data
redaction".

Each flow page in the UI lists Kubeshark filters for the run it shows. `-filters` checks those
instead of the markers: every filter must match an entry once the flows ran.

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
`0.3.1-<sanitized-branch>.g<sha7>` with the image tag as `appVersion`. Branch names are lowercased,
characters outside `a-z`, `0-9` and `-` become `-`, repeated hyphens collapse, and the result is
truncated to 100 characters and stripped of edge hyphens. An empty name becomes `dev`; a purely
numeric name gets a `branch-` prefix. Thus `functional-workloads` publishes
`0.3.1-functional-workloads.g<sha7>` with `appVersion: functional-workloads-<sha7>`.
The `g` keeps the prerelease identifier valid even when the SHA prefix is all digits with a leading
zero. Use the exact version: prereleases are not selected by Helm's default stable-version lookup.

A `vX.Y.Z` tag still publishes images tagged `vX.Y.Z` and a chart version `X.Y.Z` with
`appVersion: vX.Y.Z`. The Mirror workflow keeps `kubeshark/mirror/*` in step with the chart's
pinned server digests (run it once by hand after the first apply).

For chart-only wallet-capture development, the existing Publish workflow accepts
`workflow_dispatch` input `chart_only=true` on a branch. It runs only the scoped provider
check above, skips every image build, and publishes the same branch-development version
scheme with `appVersion: v0.3.0`. It also uploads the exact chart archive and its SHA256 as
the `wallet-capture-chart` workflow artifact. No version tag or release is created:

```bash
gh workflow run publish.yml --ref <development-branch> -f chart_only=true
```

A normal push still runs the existing full workflows; if only this manual path is desired,
use the repository's approved CI-skip commit convention before pushing. Resolve and pin
the resulting OCI manifest digest and raw archive SHA256 in the consumer workload
contract. The archive checksum and OCI manifest digest are different identities; never
substitute one for the other or fetch controller traffic from an unverified checkout.
For a remote syntax/contract check without publishing, copy the branch checkout to the
existing verification host and run only the scoped command above. Do not install a new
cluster, build images, run the full smoke workflow or alter live demo deployments for this
provider-side check.

Layout, invariants and how to add a flow: `AGENTS.md`.
