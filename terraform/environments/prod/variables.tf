variable "environment" {
  type        = string
  default     = "prod"
  description = "Target deployment environment"
}

variable "primary_region" {
  type        = string
  default     = "us-east-1"
  description = "Primary active region"
}

variable "secondary_region" {
  type        = string
  default     = "us-west-2"
  description = "Secondary hot-standby region"
}

variable "emea_region" {
  type        = string
  default     = "eu-west-1"
  description = "European Union data-residency region"
}

variable "vpc_cidr_primary" {
  type        = string
  default     = "10.100.0.0/16"
  description = "VPC CIDR for us-east-1"
}

variable "vpc_cidr_secondary" {
  type        = string
  default     = "10.101.0.0/16"
  description = "VPC CIDR for us-west-2"
}

variable "vpc_cidr_emea" {
  type        = string
  default     = "10.102.0.0/16"
  description = "VPC CIDR for eu-west-1"
}

variable "cockroach_cluster_name" {
  type        = string
  default     = "datavendor-ledger-prod"
  description = "Multi-region CockroachDB Dedicated cluster name"
}

variable "kafka_instance_type" {
  type        = string
  default     = "kafka.m5.4xlarge"
  description = "AWS MSK Broker instance size"
}
