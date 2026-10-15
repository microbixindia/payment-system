-- ==============================================================================
-- MIGRATION 002: COCKROACHDB MULTI-REGION SURVIVABILITY & GEO-PARTITIONING
-- Locality: Primary (us-east-1), Secondary (us-west-2), EMEA (eu-west-1)
-- Zero RPO / RTO with automated regional failover
-- ==============================================================================

-- 1. Configure Database Multi-Region Primary and Secondary Regions
ALTER DATABASE datavendor_ledger_prod SET PRIMARY REGION "us-east-1";
ALTER DATABASE datavendor_ledger_prod ADD REGION "us-west-2";
ALTER DATABASE datavendor_ledger_prod ADD REGION "eu-west-1";
ALTER DATABASE datavendor_ledger_prod SET SURVIVE REGION FAILURE;

-- 2. Root Ledgers & Chart of Accounts: GLOBAL TABLES
-- Reference data read frequently from any region with sub-millisecond local reads
ALTER TABLE ledgers SET LOCALITY GLOBAL;
ALTER TABLE accounts SET LOCALITY GLOBAL;

-- 3. Partitioning Journal Entries & Ledger Postings by Regional Gateway Locality
ALTER TABLE journal_entries ADD COLUMN IF NOT EXISTS region_locality crdb_internal_region;
ALTER TABLE journal_entries SET LOCALITY REGIONAL BY ROW AS region_locality;

ALTER TABLE ledger_postings ADD COLUMN IF NOT EXISTS region_locality crdb_internal_region;
ALTER TABLE ledger_postings SET LOCALITY REGIONAL BY ROW AS region_locality;

-- 4. High-Performance Survivability Tuning
ALTER TABLE journal_entries CONFIGURE ZONE USING
    num_replicas = 5,
    constraints = '{"-region=eu-west-1": 2, "+region=us-east-1": 2, "+region=us-west-2": 1}',
    lease_preferences = '[[+region=us-east-1], [+region=us-west-2]]';
