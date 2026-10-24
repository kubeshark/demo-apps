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

From then on every branch push publishes images tagged `<branch>-<sha7>`, a `vX.Y.Z` tag publishes
images and the chart, and the Mirror workflow keeps `kubeshark/mirror/*` in step with the chart's
pinned server digests (run it once by hand after the first apply).

Layout, invariants and how to add a flow: `AGENTS.md`.
