output "role_arn" {
  description = "Set as the AWS_ROLE_ARN repository variable of the GitHub repository"
  value       = aws_iam_role.github_actions.arn
}

output "repositories" {
  value = sort(concat(
    [for r in aws_ecrpublic_repository.built : r.repository_uri],
    [for r in aws_ecrpublic_repository.mirror : r.repository_uri],
    [aws_ecrpublic_repository.chart.repository_uri],
  ))
}
