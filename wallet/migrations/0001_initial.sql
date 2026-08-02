-- +goose Up
-- Double-entry ledger. The wallet lives in its own database, so tables carry
-- no prefix. Pre-release cleanup: drop the abandoned prefixed single-entry
-- tables (and their goose ledger) from the earliest iteration, if present.
DROP TABLE IF EXISTS wallet_entries;
DROP TABLE IF EXISTS wallet_accounts;
DROP TABLE IF EXISTS wallet_goose_db_version;

CREATE TABLE accounts (
    id         TEXT PRIMARY KEY,   -- 'user:{firebaseUID}' | 'revenue' | 'pending'
    type       TEXT NOT NULL CHECK (type IN ('user', 'system')),
    user_id    TEXT CHECK ((type = 'user') = (user_id IS NOT NULL)),
    balance    BIGINT NOT NULL DEFAULT 0, -- micro-rupiah, credit-positive
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX accounts_user_id_key
    ON accounts (user_id) WHERE user_id IS NOT NULL;

-- Journal header: kind, idempotency ref, and metadata belong to the
-- transaction, not to its lines.
CREATE TABLE transactions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       TEXT NOT NULL,
    ref        TEXT,
    metadata   JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX transactions_ref_key
    ON transactions (ref) WHERE ref IS NOT NULL;

-- Journal lines. Invariants (enforced by the adapter, checkable in SQL):
-- SUM(amount) = 0 per txn_id; accounts.balance = SUM(amount) per account.
CREATE TABLE entries (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    txn_id        UUID NOT NULL REFERENCES transactions(id),
    account_id    TEXT NOT NULL REFERENCES accounts(id),
    amount        BIGINT NOT NULL CHECK (amount <> 0), -- signed micro-rupiah: + in, - out
    balance_after BIGINT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX entries_account_created_idx
    ON entries (account_id, created_at DESC);

CREATE INDEX entries_txn_idx
    ON entries (txn_id);

-- System accounts are seeded here, never auto-created: revenue pairs every
-- usage debit, pending pairs gateway top-ups (and dev seeding until then).
INSERT INTO accounts (id, type) VALUES
    ('revenue', 'system'),
    ('pending', 'system');
