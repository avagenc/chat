// Package wallet is the double-entry ledger this platform books money in,
// plus the balance gate on agent routes (guard.go) and the balance the UI
// reads (handler.go). It lives at the module root rather than under
// internal/ because it is meant to serve any Avagenc product, and it knows
// nothing about what its consumers sell: turning an agent run's token usage
// into a charge lives with the agents (internal/agent), because pricing a
// model is that product's concern.
//
// Ledger is a struct, not an interface, and it is PostgreSQL. There is no
// second implementation and no port to swap one in, because the guarantees
// that make this a ledger are not expressible in Go: the sum-zero invariant
// is a deferred constraint trigger, idempotency is a unique partial index on
// transactions.ref, and serialization is row locks taken in a deterministic
// order. An interface here would promise a substitutability that could only
// ever be honoured by something weaker. Consumers that want a seam declare
// their own narrow interface on their side, sized to what they call.
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
// The ledger never interprets what a transaction pays for: Type and Metadata
// are consumer-defined, so billing something new later (API calls, payments,
// P2P transfers) is just a new type with its own posting pair.
//
// The append-only journal (transactions header + entries lines) is the only
// place a balance exists. A balance column would be a second copy of what the
// entries already say, and nothing in SQL can hold a column equal to an
// aggregate of another table — so its correctness could only ever be audited,
// never enforced. Derived, it has nothing to drift from.
//
// The schema lives in migrations/ and is applied by goose in the deploy
// pipeline (.github/workflows/deploy.yaml), never here: the runtime only
// needs DML privileges. NewLedger validates the tables exist and fails boot
// with a clear error when a deploy skipped the migration.
package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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

// Posting moves Amount into (positive) or out of (negative) one account.
type Posting struct {
	AccountID string
	// Amount in signed micro-rupiah, never zero.
	Amount int64
}

// Spec describes one transaction to record: a balanced set of postings plus
// what they were for.
type Spec struct {
	// Type labels what the transaction is: "topup", "agent_run", "refund".
	// A plain string on purpose — the set is open and the ledger never reads
	// it, so naming a type here would claim a vocabulary that belongs to
	// whoever is spending. Consumers declare their own untyped constants,
	// the way net/http does with MethodGet. Required: an unlabelled journal
	// line cannot answer what it was.
	Type string
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
// produced, carrying its transaction's type, ref, and metadata for reads.
type Entry struct {
	ID            string
	TransactionID string
	AccountID     string
	// Amount in signed micro-rupiah: positive = money in, negative = out.
	Amount       int64
	BalanceAfter int64
	Type         string
	Ref          string
	Metadata     json.RawMessage
	CreatedAt    time.Time
}

// Transaction is one recorded balanced set of entries.
type Transaction struct {
	ID       string
	Type     string
	Ref      string
	Metadata json.RawMessage
	// Entries in account-ID order, one per posting.
	Entries   []*Entry
	CreatedAt time.Time
}

// EntriesQuery filters an account's entries. Zero values mean "no filter".
type EntriesQuery struct {
	Type  string
	Since time.Time
	// Limit caps the number of entries returned; 0 = no limit.
	Limit int
}

// DB is what the ledger needs from pgx: statement execution plus
// transactions. *pgxpool.Pool satisfies it.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Ledger is the book itself, on a dedicated wallet database — so its tables
// carry no prefix.
type Ledger struct {
	db DB
}

func NewLedger(ctx context.Context, db DB) (*Ledger, error) {
	if db == nil {
		panic("wallet: NewLedger called with nil DB")
	}
	l := &Ledger{db: db}
	if err := l.validateSchema(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// Balance returns the account's balance in micros. An account with no row has
// balance 0.
func (l *Ledger) Balance(ctx context.Context, accountID string) (int64, error) {
	var balance int64
	err := l.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM entries WHERE account_id = $1`,
		accountID,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("wallet: balance %s: %w", accountID, err)
	}
	return balance, nil
}

// Transact atomically records spec's postings as one transaction: all land or
// none do. It rejects an unbalanced spec, returns ErrDuplicateRef when
// spec.Ref was already recorded, and never fails for insufficient funds —
// post-paid billing may push a user balance negative, because a billing write
// must not be lost when funds ran out.
//
// The sum-zero check below duplicates the deferred constraint trigger in
// migrations/ on purpose: this fails early with a useful error, the database
// makes it impossible for any writer to skip the check at all.
func (l *Ledger) Transact(ctx context.Context, spec Spec) (*Transaction, error) {
	if spec.Type == "" {
		return nil, fmt.Errorf("wallet: transact: empty type")
	}
	if len(spec.Postings) < 2 {
		return nil, fmt.Errorf("wallet: transact %s: got %d postings, double-entry needs at least 2", spec.Type, len(spec.Postings))
	}
	var sum int64
	for _, p := range spec.Postings {
		if p.AccountID == "" {
			return nil, fmt.Errorf("wallet: transact %s: empty account ID", spec.Type)
		}
		if p.Amount == 0 {
			return nil, fmt.Errorf("wallet: transact %s: zero amount for account %s", spec.Type, p.AccountID)
		}
		sum += p.Amount
	}
	if sum != 0 {
		return nil, fmt.Errorf("wallet: transact %s: postings sum to %d, want 0", spec.Type, sum)
	}
	// Deterministic lock order: concurrent transactions touching the same
	// accounts always take their row locks in the same sequence, so they
	// queue instead of deadlocking.
	postings := slices.Clone(spec.Postings)
	slices.SortStableFunc(postings, func(a, b Posting) int {
		return strings.Compare(a.AccountID, b.AccountID)
	})

	tx, err := l.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("wallet: transact %s: begin: %w", spec.Type, err)
	}
	defer tx.Rollback(ctx)

	txn := &Transaction{Type: spec.Type, Ref: spec.Ref, Metadata: spec.Metadata}
	err = tx.QueryRow(ctx,
		`INSERT INTO transactions (kind, ref, metadata)
		VALUES ($1, NULLIF($2, ''), $3)
		RETURNING id, created_at`,
		spec.Type, spec.Ref, metadataParam(spec.Metadata),
	).Scan(&txn.ID, &txn.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "transactions_ref_key" {
			return nil, fmt.Errorf("wallet: transact %s ref %s: %w", spec.Type, spec.Ref, ErrDuplicateRef)
		}
		return nil, fmt.Errorf("wallet: transact %s: header: %w", spec.Type, err)
	}
	txn.CreatedAt = txn.CreatedAt.UTC()

	for _, p := range postings {
		if err := lockAccount(ctx, tx, p.AccountID); err != nil {
			return nil, fmt.Errorf("wallet: transact %s: %w", spec.Type, err)
		}
		var balance int64
		err = tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount), 0) FROM entries WHERE account_id = $1`,
			p.AccountID,
		).Scan(&balance)
		if err != nil {
			return nil, fmt.Errorf("wallet: transact %s: balance %s: %w", spec.Type, p.AccountID, err)
		}
		balanceAfter := balance + p.Amount
		entry := &Entry{
			TransactionID: txn.ID,
			AccountID:     p.AccountID,
			Amount:        p.Amount,
			BalanceAfter:  balanceAfter,
			Type:          spec.Type,
			Ref:           spec.Ref,
			Metadata:      spec.Metadata,
		}
		err = tx.QueryRow(ctx,
			`INSERT INTO entries (txn_id, account_id, amount, balance_after)
			VALUES ($1, $2, $3, $4)
			RETURNING id, created_at`,
			txn.ID, p.AccountID, p.Amount, balanceAfter,
		).Scan(&entry.ID, &entry.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("wallet: transact %s: entry %s: %w", spec.Type, p.AccountID, err)
		}
		entry.CreatedAt = entry.CreatedAt.UTC()
		txn.Entries = append(txn.Entries, entry)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("wallet: transact %s: commit: %w", spec.Type, err)
	}
	return txn, nil
}

// lockAccount takes the account's row lock, which is what serializes the
// entry sums that follow it. User accounts are implicit — created by their
// first posting, and the conflicting upsert locks the row just as an update
// would; system accounts are seeded by migration, so posting to a missing one
// is a wiring bug, not a case to auto-heal.
func lockAccount(ctx context.Context, tx pgx.Tx, accountID string) error {
	if userID, currency, ok := ParseUserAccountID(accountID); ok {
		_, err := tx.Exec(ctx,
			`INSERT INTO accounts (id, type, user_id, currency) VALUES ($1, 'user', $2, $3)
			ON CONFLICT (id) DO UPDATE SET updated_at = now()`,
			accountID, userID, string(currency),
		)
		if err != nil {
			return fmt.Errorf("account %s: %w", accountID, err)
		}
		return nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("account %s: system account not seeded by migration", accountID)
	}
	if err != nil {
		return fmt.Errorf("account %s: %w", accountID, err)
	}
	return nil
}

// metadataParam maps empty metadata to NULL instead of invalid empty JSON.
func metadataParam(metadata []byte) any {
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

// Entries returns the account's entries, newest first.
func (l *Ledger) Entries(ctx context.Context, accountID string, q EntriesQuery) ([]*Entry, error) {
	sql := `SELECT e.id, e.txn_id, e.account_id, e.amount, e.balance_after, e.created_at,
			t.kind, t.ref, t.metadata
		FROM entries e
		JOIN transactions t ON t.id = e.txn_id
		WHERE e.account_id = $1`
	args := []any{accountID}
	if q.Type != "" {
		args = append(args, q.Type)
		sql += fmt.Sprintf(" AND t.kind = $%d", len(args))
	}
	if !q.Since.IsZero() {
		args = append(args, q.Since.UTC())
		sql += fmt.Sprintf(" AND e.created_at >= $%d", len(args))
	}
	sql += " ORDER BY e.created_at DESC"
	if q.Limit > 0 {
		args = append(args, q.Limit)
		sql += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	rows, err := l.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("wallet: entries %s: %w", accountID, err)
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		var (
			entry     Entry
			ref       pgtype.Text
			metadata  []byte
			createdAt time.Time
		)
		if err := rows.Scan(&entry.ID, &entry.TransactionID, &entry.AccountID, &entry.Amount, &entry.BalanceAfter, &createdAt, &entry.Type, &ref, &metadata); err != nil {
			return nil, fmt.Errorf("wallet: entries %s: scan: %w", accountID, err)
		}
		entry.Ref = ref.String
		entry.Metadata = metadata
		entry.CreatedAt = createdAt.UTC()
		entries = append(entries, &entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("wallet: entries %s: %w", accountID, err)
	}
	return entries, nil
}

func (l *Ledger) validateSchema(ctx context.Context) error {
	for _, table := range []string{"accounts", "transactions", "entries"} {
		rows, err := l.db.Query(ctx, `SELECT * FROM `+pgx.Identifier{table}.Sanitize()+` LIMIT 0`)
		if err != nil {
			return fmt.Errorf("wallet: schema validation for table %q: %w", table, err)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("wallet: schema validation for table %q: %w", table, err)
		}
	}
	return nil
}
