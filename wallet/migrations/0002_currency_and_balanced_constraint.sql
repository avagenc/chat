-- +goose Up
-- Two changes that only get cheaper the earlier they land, while the journal
-- is still empty: an account ID scheme that already carries its currency, and
-- the sum-zero invariant enforced by the database instead of the adapter.

-- One account per currency. Existing rows predate the column, and every one
-- of them is rupiah.
ALTER TABLE accounts ADD COLUMN currency TEXT NOT NULL DEFAULT 'IDR';
ALTER TABLE accounts ALTER COLUMN currency DROP DEFAULT;

-- Fold the currency into the ID so a second currency is a new account rather
-- than an exception to the naming. Entries carry the ID as a plain FK, so the
-- rename walks both tables with the constraint lifted.
ALTER TABLE entries DROP CONSTRAINT entries_account_id_fkey;
UPDATE entries SET account_id = account_id || ':IDR';
UPDATE accounts SET id = id || ':IDR';
ALTER TABLE entries ADD CONSTRAINT entries_account_id_fkey
    FOREIGN KEY (account_id) REFERENCES accounts(id);

-- A user holds at most one account per currency, not one account overall.
DROP INDEX accounts_user_id_key;
CREATE UNIQUE INDEX accounts_user_id_currency_key
    ON accounts (user_id, currency) WHERE user_id IS NOT NULL;

-- Balance stops being stored. A materialized balance is a second copy of a
-- truth the journal already holds, and no constraint can express "this column
-- equals that aggregate" — so the only way to know it never drifted is to
-- keep checking it. Derived from entries it cannot drift at all. Carrying
-- amount in the read index keeps the aggregate an index-only scan.
ALTER TABLE accounts DROP COLUMN balance;
DROP INDEX entries_account_created_idx;
CREATE INDEX entries_account_created_idx
    ON entries (account_id, created_at DESC) INCLUDE (amount);

-- The adapter already refuses unbalanced postings; this refuses them for
-- every writer, including hand-written SQL. Deferred so it judges the
-- transaction as a whole rather than the first line inserted. Mixed
-- currencies are the same violation wearing a disguise: a sum across two
-- currencies is not a number that can be zero.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assert_transaction_balanced() RETURNS TRIGGER AS $$
DECLARE
    txn        UUID;
    total      BIGINT;
    currencies INT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        txn := OLD.txn_id;
    ELSE
        txn := NEW.txn_id;
    END IF;

    SELECT SUM(e.amount), COUNT(DISTINCT a.currency)
    INTO total, currencies
    FROM entries e
    JOIN accounts a ON a.id = e.account_id
    WHERE e.txn_id = txn;

    IF total IS NULL THEN
        RETURN NULL;
    END IF;
    IF currencies > 1 THEN
        RAISE EXCEPTION 'transaction % spans % currencies', txn, currencies
            USING ERRCODE = 'check_violation';
    END IF;
    IF total <> 0 THEN
        RAISE EXCEPTION 'transaction % postings sum to %, want 0', txn, total
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER entries_transaction_balanced
    AFTER INSERT OR UPDATE OR DELETE ON entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION assert_transaction_balanced();
