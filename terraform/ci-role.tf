# Repositories created with GitHub's immutable subject claims present
# repo:<owner>@<owner-id>/<repo>@<repo-id>:... in the OIDC sub; older ones
# present repo:<owner>/<repo>:... . Both forms are trusted, the immutable one
# pinned to the IDs (gh api repos/<owner>/<repo>/actions/oidc/customization/sub).
variable "github_subjects" {
  description = "OIDC sub patterns allowed to assume the publishing role"
  type        = list(string)
  default = [
    "repo:kubeshark/demo-apps:*",
    "repo:kubeshark@99263827/demo-apps@1430858405:*",
  ]
}

data "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"
}

resource "aws_iam_role" "github_actions" {
  name = "demo-apps-github-actions"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = data.aws_iam_openid_connect_provider.github.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = { "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com" }
        StringLike   = { "token.actions.githubusercontent.com:sub" = var.github_subjects }
      }
    }]
  })
}

resource "aws_iam_role_policy" "ecr_public_push" {
  name = "ecr-public-push"
  role = aws_iam_role.github_actions.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "ecr-public:BatchCheckLayerAvailability",
          "ecr-public:CompleteLayerUpload",
          "ecr-public:InitiateLayerUpload",
          "ecr-public:PutImage",
          "ecr-public:UploadLayerPart",
          "ecr-public:DescribeImages",
        ]
        Resource = concat(
          [for r in aws_ecrpublic_repository.built : r.arn],
          [for r in aws_ecrpublic_repository.mirror : r.arn],
          [aws_ecrpublic_repository.chart.arn],
        )
      },
      {
        Effect   = "Allow"
        Action   = ["ecr-public:GetAuthorizationToken", "sts:GetServiceBearerToken"]
        Resource = "*"
      },
    ]
  })
}
