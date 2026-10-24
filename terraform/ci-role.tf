variable "github_repository" {
  description = "GitHub repository allowed to assume the publishing role"
  type        = string
  default     = "kubeshark/demo-apps"
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
        StringLike   = { "token.actions.githubusercontent.com:sub" = "repo:${var.github_repository}:*" }
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
