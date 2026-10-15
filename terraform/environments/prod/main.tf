# ==============================================================================
# DATAVENDOR PAYMENT CORE: PRODUCTION TERRAFORM TOPOLOGY
# Multi-Region Failover Architecture: Primary (us-east-1), Secondary (us-west-2), EMEA (eu-west-1)
# ==============================================================================

terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.50"
    }
    cockroach = {
      source  = "cockroachdb/cockroach"
      version = "~> 1.8"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.30"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.13"
    }
  }

  backend "s3" {
    bucket         = "datavendor-tf-state-prod-global"
    key            = "payment-core/prod/terraform.tfstate"
    region         = "us-east-1"
    encrypt        = true
    dynamodb_table = "datavendor-tf-locks-prod"
  }
}

provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
  default_tags {
    tags = {
      Environment = "prod"
      Platform    = "datavendor-payment-core"
      Compliance  = "SOC2-TypeII-PCI-DSS"
      ManagedBy   = "Terraform"
    }
  }
}

provider "aws" {
  alias  = "us_west_2"
  region = "us-west-2"
  default_tags {
    tags = {
      Environment = "prod"
      Platform    = "datavendor-payment-core"
      Compliance  = "SOC2-TypeII-PCI-DSS"
      ManagedBy   = "Terraform"
    }
  }
}

provider "aws" {
  alias  = "eu_west_1"
  region = "eu-west-1"
  default_tags {
    tags = {
      Environment = "prod"
      Platform    = "datavendor-payment-core"
      Compliance  = "SOC2-TypeII-PCI-DSS"
      ManagedBy   = "Terraform"
    }
  }
}
