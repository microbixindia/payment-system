# ==============================================================================
# DATAVENDOR MULTI-REGION ZERO-TRUST NETWORKING
# Isolated DB & Streaming Subnets with Strict Ingress Controls
# ==============================================================================

# --- PRIMARY REGION VPC (us-east-1) ---
resource "aws_vpc" "primary_vpc" {
  provider             = aws.us_east_1
  cidr_block           = var.vpc_cidr_primary
  enable_dns_hostnames = true
  enable_dns_support   = true

  tags = {
    Name = "datavendor-vpc-primary-us-east-1"
  }
}

resource "aws_subnet" "primary_private_app_1" {
  provider          = aws.us_east_1
  vpc_id            = aws_vpc.primary_vpc.id
  cidr_block        = "10.100.10.0/24"
  availability_zone = "us-east-1a"

  tags = {
    Name = "datavendor-app-us-east-1a"
    Tier = "Application"
  }
}

resource "aws_subnet" "primary_private_app_2" {
  provider          = aws.us_east_1
  vpc_id            = aws_vpc.primary_vpc.id
  cidr_block        = "10.100.11.0/24"
  availability_zone = "us-east-1b"

  tags = {
    Name = "datavendor-app-us-east-1b"
    Tier = "Application"
  }
}

resource "aws_subnet" "primary_isolated_db_1" {
  provider          = aws.us_east_1
  vpc_id            = aws_vpc.primary_vpc.id
  cidr_block        = "10.100.20.0/24"
  availability_zone = "us-east-1a"

  tags = {
    Name = "datavendor-db-us-east-1a"
    Tier = "Database-Isolated"
  }
}

resource "aws_subnet" "primary_isolated_db_2" {
  provider          = aws.us_east_1
  vpc_id            = aws_vpc.primary_vpc.id
  cidr_block        = "10.100.21.0/24"
  availability_zone = "us-east-1b"

  tags = {
    Name = "datavendor-db-us-east-1b"
    Tier = "Database-Isolated"
  }
}

# --- ZERO-TRUST SECURITY GROUPS ---

resource "aws_security_group" "edge_gateway_sg" {
  provider    = aws.us_east_1
  name        = "datavendor-edge-gateway-sg"
  description = "Ingress for Edge API Gateway with TLS termination"
  vpc_id      = aws_vpc.primary_vpc.id

  ingress {
    description = "Strict TLS 1.3 Ingress"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    description = "Allow egress to internal application subnets"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [var.vpc_cidr_primary]
  }

  tags = {
    Name = "datavendor-edge-gateway-sg"
  }
}

resource "aws_security_group" "cockroach_ledger_sg" {
  provider    = aws.us_east_1
  name        = "datavendor-cockroach-ledger-sg"
  description = "Defensive isolation for CockroachDB Core Ledger"
  vpc_id      = aws_vpc.primary_vpc.id

  ingress {
    description     = "SQL wire protocol from application tier ONLY"
    from_port       = 26257
    to_port         = 26257
    protocol        = "tcp"
    security_groups = [aws_security_group.edge_gateway_sg.id]
  }

  egress {
    description = "Reject all outbound internet traffic from database"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [var.vpc_cidr_primary]
  }

  tags = {
    Name = "datavendor-cockroach-ledger-sg"
  }
}

resource "aws_security_group" "kafka_streaming_sg" {
  provider    = aws.us_east_1
  name        = "datavendor-kafka-streaming-sg"
  description = "Private MSK Kafka streaming security group"
  vpc_id      = aws_vpc.primary_vpc.id

  ingress {
    description     = "Kafka TLS Broker connections from application tier"
    from_port       = 9094
    to_port         = 9094
    protocol        = "tcp"
    security_groups = [aws_security_group.edge_gateway_sg.id]
  }

  tags = {
    Name = "datavendor-kafka-streaming-sg"
  }
}
