terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  backend "s3" {
    bucket = "kubeshark-terraform-state-kubeshark"
    key    = "demo-apps"
    region = "us-east-1"
  }
}

# ECR Public exists only in us-east-1.
provider "aws" {
  region = "us-east-1"
}
