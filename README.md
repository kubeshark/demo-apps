# demo-apps

Workloads that exercise every protocol dissector Kubeshark ships, grouped into flows a presenter
can trigger from a small UI and that also run on a timer. One real server per protocol, one Go
service per flow group talking to it through the protocol's real client library, and a declared
payload per flow so what was captured can be asserted, not only watched.

Images are published to ECR Public and the Helm chart as an OCI artifact, so the set installs on
any cluster without credentials.

```bash
helm install demo-apps oci://public.ecr.aws/l3y1d4r0/kubeshark/charts/demo-apps -n demo-apps --create-namespace
```

Layout, invariants and how to add a flow: `AGENTS.md`.
