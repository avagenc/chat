package postgres

// Integration test against a real PostgreSQL database, per the repo
// convention (behavior over mocks). Skipped unless WALLET_TEST_DB_URL is
// set. Every run DROPs the wallet tables and reapplies migrations/ from
// scratch — point it at a disposable database, never at a shared one.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/avagenc/chat/internal/wallet"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newTestLedger(t *testing.T) (*Ledger, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("WALLET_TEST_DB_URL")
	if url == "" {
		t.Skip("WALLET_TEST_DB_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, table := range []string{"entries", "transactions", "accounts"} {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	// The goose directives in the migration files are plain SQL comments, so
	// applying them is just executing the files in order.
	migrations, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	slices.Sort(migrations)
	if len(migrations) == 0 {
		t.Fatal("no migration files found")
	}
	for _, path := range migrations {
		sql, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}

	ledger, err := NewLedger(ctx, pool)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}
	return ledger, pool
}

func mustBalance(t *testing.T, ledger *Ledger, accountID string) int64 {
	t.Helper()
	balance, err := ledger.Balance(context.Background(), accountID)
	if err != nil {
		t.Fatalf("balance %s: %v", accountID, err)
	}
	return balance
}

func TestLedger(t *testing.T) {
	ledger, pool := newTestLedger(t)
	ctx := context.Background()
	alice := wallet.UserAccountID("alice")

	// An account with no row reads as balance 0, not an error.
	if got := mustBalance(t, ledger, alice); got != 0 {
		t.Fatalf("fresh balance = %d, want 0", got)
	}

	// Top-up: credit alice against pending, with an idempotency ref.
	txn, err := ledger.Transact(ctx, wallet.Spec{
		Kind:     "topup",
		Ref:      "order-1",
		Metadata: json.RawMessage(`{"source":"test"}`),
		Postings: []wallet.Posting{
			{AccountID: alice, Amount: 100_000_000},
			{AccountID: "pending", Amount: -100_000_000},
		},
	})
	if err != nil {
		t.Fatalf("topup: %v", err)
	}
	if txn.ID == "" || len(txn.Entries) != 2 {
		t.Fatalf("topup txn = id %q with %d entries, want id and 2 entries", txn.ID, len(txn.Entries))
	}
	if got := mustBalance(t, ledger, alice); got != 100_000_000 {
		t.Fatalf("alice after topup = %d, want 100_000_000", got)
	}
	if got := mustBalance(t, ledger, "pending"); got != -100_000_000 {
		t.Fatalf("pending after topup = %d, want -100_000_000", got)
	}

	// Replaying the same ref rejects the whole transaction with the sentinel
	// and writes nothing.
	_, err = ledger.Transact(ctx, wallet.Spec{
		Kind: "topup",
		Ref:  "order-1",
		Postings: []wallet.Posting{
			{AccountID: alice, Amount: 100_000_000},
			{AccountID: "pending", Amount: -100_000_000},
		},
	})
	if !errors.Is(err, wallet.ErrDuplicateRef) {
		t.Fatalf("duplicate ref error = %v, want ErrDuplicateRef", err)
	}
	var txnCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM transactions`).Scan(&txnCount); err != nil {
		t.Fatalf("count transactions: %v", err)
	}
	if txnCount != 1 {
		t.Fatalf("transactions after duplicate ref = %d, want 1", txnCount)
	}

	// Usage charge: debit alice against revenue, receipt in metadata.
	receipt := json.RawMessage(`{"agent":"ava","tokens":{"total":42}}`)
	if _, err := ledger.Transact(ctx, wallet.Spec{
		Kind:     wallet.KindAgentRun,
		Metadata: receipt,
		Postings: []wallet.Posting{
			{AccountID: alice, Amount: -30_000_000},
			{AccountID: wallet.AccountRevenue, Amount: 30_000_000},
		},
	}); err != nil {
		t.Fatalf("agent_run: %v", err)
	}
	if got := mustBalance(t, ledger, alice); got != 70_000_000 {
		t.Fatalf("alice after charge = %d, want 70_000_000", got)
	}
	if got := mustBalance(t, ledger, wallet.AccountRevenue); got != 30_000_000 {
		t.Fatalf("revenue after charge = %d, want 30_000_000", got)
	}

	// Entries: newest first, kind filter works, and each line carries its
	// transaction's kind and metadata.
	entries, err := ledger.Entries(ctx, alice, wallet.EntriesQuery{Kind: wallet.KindAgentRun})
	if err != nil {
		t.Fatalf("entries by kind: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("agent_run entries = %d, want 1", len(entries))
	}
	charge := entries[0]
	if charge.Amount != -30_000_000 || charge.BalanceAfter != 70_000_000 || charge.TransactionID == "" {
		t.Fatalf("charge entry = amount %d balance_after %d txn %q", charge.Amount, charge.BalanceAfter, charge.TransactionID)
	}
	// JSONB normalizes formatting, so compare the decoded value, like the
	// usage endpoint does.
	var decoded struct {
		Agent  string `json:"agent"`
		Tokens struct {
			Total int64 `json:"total"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(charge.Metadata, &decoded); err != nil {
		t.Fatalf("unmarshal charge metadata: %v", err)
	}
	if decoded.Agent != "ava" || decoded.Tokens.Total != 42 {
		t.Fatalf("charge metadata = %s, want agent ava with 42 total tokens", charge.Metadata)
	}
	all, err := ledger.Entries(ctx, alice, wallet.EntriesQuery{})
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(all) != 2 || all[0].Kind != wallet.KindAgentRun || all[1].Kind != "topup" {
		t.Fatalf("entries order/kinds wrong: %+v", all)
	}
	limited, err := ledger.Entries(ctx, alice, wallet.EntriesQuery{Limit: 1})
	if err != nil {
		t.Fatalf("entries limit: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited entries = %d, want 1", len(limited))
	}

	// Post-paid: a debit may push the user balance negative and must not fail.
	if _, err := ledger.Transact(ctx, wallet.Spec{
		Kind: wallet.KindAgentRun,
		Postings: []wallet.Posting{
			{AccountID: alice, Amount: -200_000_000},
			{AccountID: wallet.AccountRevenue, Amount: 200_000_000},
		},
	}); err != nil {
		t.Fatalf("overdraft charge: %v", err)
	}
	if got := mustBalance(t, ledger, alice); got != -130_000_000 {
		t.Fatalf("alice after overdraft = %d, want -130_000_000", got)
	}

	// Ledger invariants: every transaction sums to zero, every materialized
	// balance matches its entries, and the whole ledger sums to zero.
	var broken int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT txn_id FROM entries GROUP BY txn_id HAVING SUM(amount) <> 0) b`,
	).Scan(&broken); err != nil {
		t.Fatalf("txn invariant query: %v", err)
	}
	if broken != 0 {
		t.Fatalf("%d transactions do not sum to zero", broken)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM accounts a
		LEFT JOIN (SELECT account_id, SUM(amount) s FROM entries GROUP BY account_id) e
			ON a.id = e.account_id
		WHERE a.balance <> COALESCE(e.s, 0)`,
	).Scan(&broken); err != nil {
		t.Fatalf("account invariant query: %v", err)
	}
	if broken != 0 {
		t.Fatalf("%d accounts diverge from their entries", broken)
	}
	var total int64
	if err := pool.QueryRow(ctx, `SELECT SUM(balance) FROM accounts`).Scan(&total); err != nil {
		t.Fatalf("global invariant query: %v", err)
	}
	if total != 0 {
		t.Fatalf("ledger total = %d, want 0", total)
	}
}

func TestTransactRejects(t *testing.T) {
	ledger, pool := newTestLedger(t)
	ctx := context.Background()
	alice := wallet.UserAccountID("alice")

	for name, spec := range map[string]wallet.Spec{
		"empty kind": {Postings: []wallet.Posting{
			{AccountID: alice, Amount: -1}, {AccountID: wallet.AccountRevenue, Amount: 1},
		}},
		"single posting": {Kind: "topup", Postings: []wallet.Posting{
			{AccountID: alice, Amount: 1},
		}},
		"zero amount": {Kind: "topup", Postings: []wallet.Posting{
			{AccountID: alice, Amount: 0}, {AccountID: "pending", Amount: 0},
		}},
		"empty account": {Kind: "topup", Postings: []wallet.Posting{
			{AccountID: "", Amount: 1}, {AccountID: "pending", Amount: -1},
		}},
		"unbalanced": {Kind: "topup", Postings: []wallet.Posting{
			{AccountID: alice, Amount: 2}, {AccountID: "pending", Amount: -1},
		}},
		"unseeded system account": {Kind: "settlement", Postings: []wallet.Posting{
			{AccountID: "bank", Amount: 1}, {AccountID: "pending", Amount: -1},
		}},
	} {
		if _, err := ledger.Transact(ctx, spec); err == nil {
			t.Errorf("%s: Transact succeeded, want error", name)
		}
	}

	// Rejections must leave no partial writes behind — not even the header.
	var txns, entries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM transactions`).Scan(&txns); err != nil {
		t.Fatalf("count transactions: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM entries`).Scan(&entries); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if txns != 0 || entries != 0 {
		t.Fatalf("rejected specs left %d transactions and %d entries", txns, entries)
	}
}
