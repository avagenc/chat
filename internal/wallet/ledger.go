// Package wallet is the money feature of the app, one vertical slice: the
// double-entry ledger contract (this file) implemented by the postgres
// subpackage, the biller that turns an agent run's token usage into a
// balanced transaction (biller.go), the balance gate on agent routes
// (guard.go), and the read endpoints the UI consumes (handler.go).
//
// Amounts are int64 micro-rupiah (1 IDR = 1_000_000 micros) — exact integer
// arithmetic, no floats. The sign convention is credit-positive: a positive
// amount moves money into an account, a negative amount moves it out, and a
// transaction's postings always sum to zero. User and revenue balances read
// positive; asset-like system accounts (pending) accumulate the negative
// mirror — that is double-entry working, not a bug.
//
// The ledger never interprets what a transaction pays for: Kind and Metadata
// are consumer-defined, so billing something new later (API calls, payments,
// P2P transfers) is just a new kind with its own posting pair. The schema
// lives in postgres/migrations, applied by goose in the deploy pipeline —
// not at boot; the adapter only validates it (see WALLET.md).
package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// UserAccountID constructs the deterministic ledger account ID for a
// Firebase UID. User accounts are implicit: no create endpoint, the first
// posting creates the row, a missing row reads as balance 0.
func UserAccountID(userID string) string { return "user:" + userID }

// AccountRevenue is the system account credited by every usage debit.
// System accounts are seeded by migration, never auto-created; the full
// chart of accounts lives in WALLET.md.
const AccountRevenue = "revenue"

// AccountPending is the system account debited by every top-up: money the
// payment gateway holds that has not settled to a bank account yet.
// Asset-like, so it reads negative — the credit-positive double-entry mirror.
const AccountPending = "pending"

// Kind labels what a transaction is (e.g. "topup", "agent_run", "refund").
// Open set: consumers define their own kinds, the wallet never interprets
// them.
type Kind string

// Posting moves Amount into (positive) or out of (negative) one account.
type Posting struct {
	AccountID string
	// Amount in signed micro-rupiah, never zero.
	Amount int64
}

// Spec describes one transaction to record: a balanced set of postings plus
// what they were for.
type Spec struct {
	Kind Kind
	// Ref is an optional idempotency key, unique across all transactions
	// when set.
	Ref string
	// Metadata is opaque to the wallet and stored verbatim.
	Metadata json.RawMessage
	// Postings must number at least two and sum to zero.
	Postings []Posting
}

// ErrDuplicateRef is the sentinel a Ledger returns when Spec.Ref matches an
// already-recorded transaction. It is the idempotency hook for
// payment-gateway webhooks: retrying a top-up with the same Ref is a no-op
// the consumer detects with errors.Is.
var ErrDuplicateRef = errors.New("wallet: duplicate ref")

// Entry is one recorded ledger line: a posting with the account balance it
// produced, carrying its transaction's kind, ref, and metadata for reads.
type Entry struct {
	ID            string
	TransactionID string
	AccountID     string
	// Amount in signed micro-rupiah: positive = money in, negative = out.
	Amount       int64
	BalanceAfter int64
	Kind         Kind
	Ref          string
	Metadata     json.RawMessage
	CreatedAt    time.Time
}

// Transaction is one recorded balanced set of entries.
type Transaction struct {
	ID       string
	Kind     Kind
	Ref      string
	Metadata json.RawMessage
	// Entries in account-ID order, one per posting.
	Entries   []*Entry
	CreatedAt time.Time
}

// EntriesQuery filters an account's entries. Zero values mean "no filter".
type EntriesQuery struct {
	Kind  Kind
	Since time.Time
	// Limit caps the number of entries returned; 0 = no limit.
	Limit int
}

// Ledger is the wallet port.
type Ledger interface {
	// Balance returns the account's balance in micro-rupiah. An account
	// with no row has balance 0.
	Balance(ctx context.Context, accountID string) (int64, error)
	// Transact atomically records spec's postings as one transaction: all
	// land or none do. It rejects an unbalanced spec, returns
	// ErrDuplicateRef when spec.Ref was already recorded, and never fails
	// for insufficient funds — post-paid billing may push a user balance
	// negative, because a billing write must not be lost when funds ran
	// out.
	Transact(ctx context.Context, spec Spec) (*Transaction, error)
	// Entries returns the account's entries, newest first.
	Entries(ctx context.Context, accountID string, q EntriesQuery) ([]*Entry, error)
}
