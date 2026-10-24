locals {
  values = yamldecode(file("${path.module}/../deploy/chart/values.yaml"))

  # One repository per image built here: every cmd/<service> (ko) and every
  # clients/<lang>/Dockerfile, named the way the chart resolves them.
  services = [for f in fileset("${path.module}/../cmd", "*/main.go") : dirname(f)]
  clients  = [for f in fileset("${path.module}/../clients", "*/Dockerfile") : "client-${dirname(f)}"]
  built    = toset(concat(local.services, local.clients))

  # One repository per third-party server, named by servers.<x>.image.name.
  mirrors = toset([for s in values(local.values.servers) : s.image.name])
}

# ECR Public has no lifecycle policies, so nothing here can expire an image.
resource "aws_ecrpublic_repository" "built" {
  for_each        = local.built
  repository_name = "kubeshark/demo-apps/${each.key}"

  catalog_data {
    description       = "demo-apps ${each.key}: part of the workload set that exercises every Kubeshark dissector"
    about_text        = "Built from https://github.com/kubeshark/demo-apps. Install with the demo-apps Helm chart."
    architectures     = ["x86-64"]
    operating_systems = ["Linux"]
  }
}

resource "aws_ecrpublic_repository" "mirror" {
  for_each        = local.mirrors
  repository_name = "kubeshark/mirror/${each.key}"

  catalog_data {
    description       = "Unmodified copy of the upstream ${each.key} image used by the demo-apps chart"
    about_text        = "Copied by digest from its upstream registry so the demo-apps chart installs from one registry."
    architectures     = ["x86-64"]
    operating_systems = ["Linux"]
  }
}

resource "aws_ecrpublic_repository" "chart" {
  repository_name = "kubeshark/charts/demo-apps"

  catalog_data {
    description = "demo-apps Helm chart (OCI)"
    about_text  = "helm install demo-apps oci://public.ecr.aws/<alias>/kubeshark/charts/demo-apps"
  }
}
