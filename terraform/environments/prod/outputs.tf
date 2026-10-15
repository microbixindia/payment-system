output "primary_vpc_id" {
  description = "The ID of the primary VPC in us-east-1"
  value       = aws_vpc.primary_vpc.id
}

output "cockroach_cluster_id" {
  description = "CockroachDB Dedicated multi-region cluster ID"
  value       = cockroach_cluster.ledger_cluster.id
}

output "kafka_bootstrap_brokers_tls" {
  description = "TLS connection host string for AWS MSK Kafka"
  value       = aws_msk_cluster.telemetry_kafka.bootstrap_brokers_tls
}

output "eks_cluster_name" {
  description = "The name of the primary EKS cluster"
  value       = module.eks_primary.cluster_name
}
