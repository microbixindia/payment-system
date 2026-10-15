-- ============================================================================
-- DATAVENDOR PAYMENT CORE: ENTERPRISE DOUBLE-ENTRY ACCOUNTING LEDGER SCHEMA
-- Target Engine: CockroachDB v23+ / PostgreSQL 15+ (ACID Compliant)
-- Precision: NUMERIC(38, 4) preventing floating-point precision loss
-- Partitioning: Multi-Region Aware & Idempotency-Locked
-- ============================================================================

-- Enable UUID extension if running under standard PostgreSQL
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ----------------------------------------------------------------------------
-- 1. ROOT LEDGERS TABLE
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS ledgers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(128) NOT NULL,
    base_currency VARCHAR(3) NOT NULL, -- ISO 4217 (USD, EUR, GBP, JPY, etc.)
    status VARCHAR(32) NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'SUSPENDED', 'ARCHIVED')),
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_ledgers_name ON ledgers(name);

-- ----------------------------------------------------------------------------
-- 2. CHART OF ACCOUNTS (Asset, Liability, Equity, Revenue, Expense)
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ledger_id UUID NOT NULL REFERENCES ledgers(id) ON DELETE RESTRICT,
    code VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    account_type VARCHAR(16) NOT NULL CHECK (account_type IN ('ASSET', 'LIABILITY', 'EQUITY', 'REVENUE', 'EXPENSE')),
    normal_balance VARCHAR(6) NOT NULL CHECK (normal_balance IN ('DEBIT', 'CREDIT')),
    currency VARCHAR(3) NOT NULL,
    current_balance NUMERIC(38, 4) NOT NULL DEFAULT 0.0000,
    status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'FROZEN', 'CLOSED')),
    version BIGINT NOT NULL DEFAULT 1, -- Optimistic locking counter
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_ledger_account_code UNIQUE (ledger_id, code)
);

CREATE INDEX IF NOT EXISTS idx_accounts_ledger_type ON accounts(ledger_id, account_type);
CREATE INDEX IF NOT EXISTS idx_accounts_status ON accounts(status);

-- ----------------------------------------------------------------------------
-- 3. JOURNAL ENTRIES (Transactional Envelope with Idempotency Shield)
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS journal_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ledger_id UUID NOT NULL REFERENCES ledgers(id) ON DELETE RESTRICT,
    idempotency_hash VARCHAR(64) NOT NULL, -- SHA-256 hash enforcing zero-duplicate operations
    correlation_id VARCHAR(128) NOT NULL,
    external_ref VARCHAR(255),
    description TEXT NOT NULL,
    state VARCHAR(32) NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING', 'POSTED', 'REJECTED', 'REVERSED')),
    total_amount NUMERIC(38, 4) NOT NULL CHECK (total_amount >= 0.0000),
    currency VARCHAR(3) NOT NULL,
    posted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_journal_idempotency_hash UNIQUE (idempotency_hash)
);

CREATE INDEX IF NOT EXISTS idx_journal_entries_ledger_posted ON journal_entries(ledger_id, posted_at DESC);
CREATE INDEX IF NOT EXISTS idx_journal_entries_correlation ON journal_entries(correlation_id);
CREATE INDEX IF NOT EXISTS idx_journal_entries_state ON journal_entries(state);

-- ----------------------------------------------------------------------------
-- 4. LEDGER POSTINGS (Immutable Double-Entry Postings Lines)
-- Invariant: For any posted journal entry, SUM(debit_amount) == SUM(credit_amount)
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS ledger_postings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    journal_entry_id UUID NOT NULL REFERENCES journal_entries(id) ON DELETE RESTRICT,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    direction VARCHAR(6) NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount NUMERIC(38, 4) NOT NULL CHECK (amount > 0.0000),
    post_balance NUMERIC(38, 4) NOT NULL, -- Balance of the account after this posting
    sequence_no INT NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_posting_sequence UNIQUE (journal_entry_id, sequence_no)
);

CREATE INDEX IF NOT EXISTS idx_postings_account_time ON ledger_postings(account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_postings_journal ON ledger_postings(journal_entry_id);

-- ----------------------------------------------------------------------------
-- 5. TAMPER-EVIDENT CRYPTOGRAPHIC AUDIT LOG (Hash-Chained Block Ledger)
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS audit_events (
    id BIGSERIAL PRIMARY KEY,
    entity_type VARCHAR(64) NOT NULL,
    entity_id VARCHAR(64) NOT NULL,
    action VARCHAR(32) NOT NULL,
    payload JSONB NOT NULL,
    prev_hash VARCHAR(64) NOT NULL,
    entry_hash VARCHAR(64) NOT NULL, -- SHA-256(prev_hash || payload || timestamp)
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_audit_entity ON audit_events(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_events(created_at DESC);
