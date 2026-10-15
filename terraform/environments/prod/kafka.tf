# ==============================================================================
# AWS MANAGED STREAMING FOR APACHE KAFKA (MSK)
# Encrypted-at-Rest via KMS CMK, TLS In-Transit Only
# ==============================================================================

resource "aws_kms_key" "msk_encryption_key" {
  provider                = aws.us_east_1
  description             = "KMS Customer Managed Key for MSK telemetry logs"
  deletion_window_in_days = 30
  enable_key_rotation     = true

  tags = {
    Name = "datavendor-msk-cmk"
  }
}

resource "aws_msk_cluster" "telemetry_kafka" {
  provider               = aws.us_east_1
  cluster_name           = "datavendor-telemetry-kafka-prod"
  kafka_version          = "3.6.0"
  number_of_broker_nodes = 6

  broker_node_group_info {
    instance_type   = var.kafka_instance_type
    client_subnets  = [
      aws_subnet.primary_private_app_1.id,
      aws_subnet.primary_private_app_2.id,
    ]
    security_groups = [aws_security_group.kafka_streaming_sg.id]
    storage_info {
      ebs_storage_info {
        volume_size = 5000 # 5 TB per broker
      }
    }
  }

  encryption_info {
    encryption_at_rest_kms_key_arn = aws_kms_key.msk_encryption_key.arn
    encryption_in_transit {
      client_broker = "TLS"
      in_cluster    = true
    }
  }

  client_authentication {
    sasl {
      scram = true
      iam   = true
    }
  }

  logging_info {
    broker_logs {
      cloudwatch_logs {
        enabled   = true
        log_group = "/aws/msk/datavendor-telemetry-prod"
      }
    }
  }

  tags = {
    Name = "datavendor-telemetry-kafka-prod"
  }
}
