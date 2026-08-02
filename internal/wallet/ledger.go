// Package wallet is the money feature of the app, one vertical slice: the
// double-entry ledger contract (this file) implemented by the postgres
// subpackage, the balance gate on agent routes (guard.go), and the balance
// the UI reads (handler.go).
//
// It knows nothing about what its consumers sell. Turning an agent run's
// token usage into a charge lives with the agents (internal/agent), because
// pricing a model is this product's concern while the books are meant to
// serve every Avagenc product — the same reason this package is the piece
// that could one day move behind a network call on its own.
//
// Amounts are int64 micros: one millionth of the account's currency unit
// (1 IDR = 1_000_000 micros) — exact integer arithmetic, no floats. The unit
// is currency-agnostic; which currency a number is in comes from the account
// it posts to. The sign convention is credit-positive: a positive
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
	"strings"
	"time"
)

// Currency is the unit an account is denominated in, ISO 4217. It lives on
// the account rather than the posting because the sum-zero invariant is only
// meaningful within one currency: a transaction's postings must all share
// one, and converting between two is a transaction through an FX account,
// never a posting that spans both.
type Currency string

const IDR Currency = "IDR"

// UserAccountID constructs the deterministic ledger account ID for a Firebase
// UID. User accounts are implicit: no create endpoint, the first posting
// creates the row, a missing row reads as balance 0. One account per
// currency, so the currency is part of the ID — a user holding two currencies
// holds two accounts, never one account with two balances.
func UserAccountID(userID string, currency Currency) string {
	return "user:" + userID + ":" + string(currency)
}

// ParseUserAccountID splits an ID built by UserAccountID, and reports false
// for system accounts. It lives beside its constructor so the ID format is
// written down once; the adapter needs the parts to create the row implicitly
// on first posting. Splitting at the last colon is safe because Firebase UIDs
// are alphanumeric.
func ParseUserAccountID(accountID string) (userID string, currency Currency, ok bool) {
	rest, ok := strings.CutPrefix(accountID, "user:")
	if !ok {
		return "", "", false
	}
	i := strings.LastIndex(rest, ":")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], Currency(rest[i+1:]), true
}

// RevenueAccountID is the system account credited by every usage debit.
// System accounts are seeded by migration, never auto-created; the full
// chart of accounts lives in WALLET.md.
func RevenueAccountID(currency Currency) string { return "revenue:" + string(currency) }

// PendingAccountID is the system account debited by every top-up: money the
// payment gateway holds that has not settled to a bank account yet.
// Asset-like, so it reads negative — the credit-positive double-entry mirror.
func PendingAccountID(currency Currency) string { return "pending:" + string(currency) }

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
