-- ==============================================================================
-- MIGRATION 003: CRYPTOGRAPHIC MERKLE TREE AUDIT LOG & BLOCK ROOT RECORDING
-- Guarantees mathematically tamper-evident transaction batch verification
-- ==============================================================================

CREATE TABLE IF NOT EXISTS merkle_audit_blocks (
    block_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ledger_id UUID NOT NULL REFERENCES ledgers(id) ON DELETE RESTRICT,
    block_height BIGINT NOT NULL,
    merkle_root_hash VARCHAR(64) NOT NULL, -- 256-bit SHA-256 root digest
    prev_block_hash VARCHAR(64) NOT NULL,
    transaction_count INT NOT NULL CHECK (transaction_count > 0),
    ed25519_signature VARCHAR(128) NOT NULL, -- 256-bit asymmetric digital signature
    signer_public_key VARCHAR(64) NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_merkle_block_height UNIQUE (ledger_id, block_height)
);

CREATE INDEX IF NOT EXISTS idx_merkle_root_hash ON merkle_audit_blocks(merkle_root_hash);
CREATE INDEX IF NOT EXISTS idx_merkle_block_time ON merkle_audit_blocks(committed_at DESC);

-- Transaction inclusion table recording leaf positions within Merkle blocks
CREATE TABLE IF NOT EXISTS merkle_leaf_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    block_id UUID NOT NULL REFERENCES merkle_audit_blocks(block_id) ON DELETE CASCADE,
    journal_entry_id UUID NOT NULL REFERENCES journal_entries(id) ON DELETE RESTRICT,
    leaf_index INT NOT NULL,
    leaf_hash VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_block_leaf_index UNIQUE (block_id, leaf_index)
);

CREATE INDEX IF NOT EXISTS idx_merkle_journal ON merkle_leaf_entries(journal_entry_id);
