# demo-apps - invariants

This file states facts a reader needs before changing anything. It is kept short on purpose.

## Layout

```
cmd/<service>        one Go binary per service, one image per binary
internal/            shared Go code (flows, markers, config)
proto/               protobuf sources; generated Go is committed, CI checks drift
clients/<lang>       non-Go TLS clients (java, python, node)
deploy/chart         the Helm chart, the only deployment definition
terraform/           ECR Public repositories and the CI role, applied by hand
corpus/              expected per-dissector entry floors for coverage checks
```

There is no generated aggregate manifest. The chart is the single description of every workload.

## Registry

Every image resolves through one name: `public.ecr.aws/l3y1d4r0/kubeshark/demo-apps/<service>`
for images built here, `public.ecr.aws/l3y1d4r0/kubeshark/mirror/<upstream>` for third-party
servers. Mirrors are copied by digest from `mirrors.yaml`; the chart pins the same digests. No
repository carries an expiry lifecycle rule. No image is tagged `latest`.

## Flows

A flow is a directory under `internal/flows/<name>` with a `markers.tsv`
(`kind<TAB>value<TAB>what`, kinds `keep` and `vanish`). The service embeds the file and puts every
value on the wire; a checker reads the same file. Enabling `flows.<name>.enabled` in the chart
renders the services and servers that flow needs.

Per-flow table (port, image, servers, what it declares) is filled in as flows land.

## Dissector constraints the drivers respect

- Redis: RESP2 only, clients must not negotiate RESP3.
- MongoDB: wire compression off.
- PostgreSQL plaintext flows use `sslmode=disable`; TLS variants rely on the tracer.
- Kafka: client API versions pinned to what the dissector decodes; the unpinned scenario is off by default.
- HTTP/2 needs the client preface or an `h2c` upgrade; WebSocket enters only via an HTTP/1.1 upgrade.
- LDAP: no StartTLS; LDAPS relies on the tracer.
