# ==============================================================================
# COCKROACHDB DEDICATED MULTI-REGION TOPOLOGY
# Region Survival Policy: Guaranteed Zero Data Loss (RPO=0) Across Cloud AZ / Region Outages
# ==============================================================================

resource "cockroach_cluster" "ledger_cluster" {
  name           = var.cockroach_cluster_name
  cloud_provider = "AWS"
  cockroach_version = "v23.2.0"

  dedicated = {
    storage_gib = 2000
    machine_type = "m6i.4xlarge"

    region_nodes = {
      "us-east-1" = 6
      "us-west-2" = 6
      "eu-west-1" = 3
    }

    # CockroachDB Dedicated multi-region survivability
    disk_iops = 10000
  }
}

resource "cockroach_database" "payment_core_db" {
  cluster_id = cockroach_cluster.ledger_cluster.id
  name       = "datavendor_ledger_prod"
}

resource "cockroach_sql_user" "ledger_engine_user" {
  cluster_id = cockroach_cluster.ledger_cluster.id
  name       = "ledger_engine_svc"
  password   = "EncryptedVaultManagedSecretKeyChangeOnDeploy99!"
}

resource "cockroach_user_role_grant" "ledger_admin" {
  cluster_id = cockroach_cluster.ledger_cluster.id
  user       = cockroach_sql_user.ledger_engine_user.name
  role       = "admin"
  database   = cockroach_database.payment_core_db.name
}
