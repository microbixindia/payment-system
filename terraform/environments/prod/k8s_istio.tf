# ==============================================================================
# EKS & ISTIO SERVICE MESH TOPOLOGY
# Zero-Trust Mutual TLS (mTLS) with Strict PeerAuthentication
# ==============================================================================

module "eks_primary" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"

  cluster_name    = "datavendor-payment-eks-prod"
  cluster_version = "1.30"

  vpc_id     = aws_vpc.primary_vpc.id
  subnet_ids = [
    aws_subnet.primary_private_app_1.id,
    aws_subnet.primary_private_app_2.id,
  ]

  cluster_endpoint_public_access  = false
  cluster_endpoint_private_access = true

  eks_managed_node_groups = {
    payment_workloads = {
      name           = "payment-compute-nodegroup"
      instance_types = ["c6i.4xlarge"]

      min_size     = 6
      max_size     = 30
      desired_size = 12

      labels = {
        tier = "payment-core"
      }
    }
  }

  tags = {
    Name = "datavendor-payment-eks-prod"
  }
}
