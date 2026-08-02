// Package postgres implements the wallet.Ledger port on PostgreSQL — a
// dedicated wallet database, so tables carry no prefix. The append-only
// journal (transactions header + entries lines) is the only place a balance
// exists: Transact writes the header, then walks the postings in account-ID
// order — a deterministic lock order, so concurrent transactions touching the
// same accounts queue instead of deadlocking — locking each account row
// before summing its entries, so the balance_after it records is the one that
// row lock guarantees.
//
// A balance column would be a second copy of what the entries already say,
// and nothing in SQL can hold a column equal to an aggregate of another
// table — so its correctness could only ever be audited, never enforced.
// Derived, it has nothing to drift from. The remaining invariant,
// SUM(entries.amount) == 0 per txn_id, is enforced by a deferred constraint
// trigger (see migrations/) rather than by the validation below: this adapter
// checks it to fail early with a useful error, the database checks it so no
// writer can skip the check at all.
//
// The schema lives in migrations/ and is applied by goose in the deploy
// pipeline (.github/workflows/deploy.yaml), never here: the runtime only
// needs DML privileges. NewLedger validates the tables exist and fails boot
// with a clear error when a deploy skipped the migration.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/avagenc/chat/internal/wallet"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DB is what the ledger needs from pgx: statement execution plus
// transactions. *pgxpool.Pool satisfies it.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Ledger struct {
	db DB
}

var _ wallet.Ledger = (*Ledger)(nil)

func NewLedger(ctx context.Context, db DB) (*Ledger, error) {
	if db == nil {
		panic("postgres: NewLedger called with nil DB")
	}
	l := &Ledger{db: db}
	if err := l.validateSchema(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Ledger) Balance(ctx context.Context, accountID string) (int64, error) {
	var balance int64
	err := l.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM entries WHERE account_id = $1`,
		accountID,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("postgres: balance %s: %w", accountID, err)
	}
	return balance, nil
}

func (l *Ledger) Transact(ctx context.Context, spec wallet.Spec) (*wallet.Transaction, error) {
	if spec.Kind == "" {
		return nil, fmt.Errorf("postgres: transact: empty kind")
	}
	if len(spec.Postings) < 2 {
		return nil, fmt.Errorf("postgres: transact %s: got %d postings, double-entry needs at least 2", spec.Kind, len(spec.Postings))
	}
	var sum int64
	for _, p := range spec.Postings {
		if p.AccountID == "" {
			return nil, fmt.Errorf("postgres: transact %s: empty account ID", spec.Kind)
		}
		if p.Amount == 0 {
			return nil, fmt.Errorf("postgres: transact %s: zero amount for account %s", spec.Kind, p.AccountID)
		}
		sum += p.Amount
	}
	if sum != 0 {
		return nil, fmt.Errorf("postgres: transact %s: postings sum to %d, want 0", spec.Kind, sum)
	}
	// Deterministic lock order: concurrent transactions touching the same
	// accounts always take their row locks in the same sequence, so they
	// queue instead of deadlocking.
	postings := slices.Clone(spec.Postings)
	slices.SortStableFunc(postings, func(a, b wallet.Posting) int {
		return strings.Compare(a.AccountID, b.AccountID)
	})

	tx, err := l.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: transact %s: begin: %w", spec.Kind, err)
	}
	defer tx.Rollback(ctx)

	txn := &wallet.Transaction{Kind: spec.Kind, Ref: spec.Ref, Metadata: spec.Metadata}
	err = tx.QueryRow(ctx,
		`INSERT INTO transactions (kind, ref, metadata)
		VALUES ($1, NULLIF($2, ''), $3)
		RETURNING id, created_at`,
		string(spec.Kind), spec.Ref, metadataParam(spec.Metadata),
	).Scan(&txn.ID, &txn.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "transactions_ref_key" {
			return nil, fmt.Errorf("postgres: transact %s ref %s: %w", spec.Kind, spec.Ref, wallet.ErrDuplicateRef)
		}
		return nil, fmt.Errorf("postgres: transact %s: header: %w", spec.Kind, err)
	}
	txn.CreatedAt = txn.CreatedAt.UTC()

	for _, p := range postings {
		if err := lockAccount(ctx, tx, p.AccountID); err != nil {
			return nil, fmt.Errorf("postgres: transact %s: %w", spec.Kind, err)
		}
		var balance int64
		err = tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount), 0) FROM entries WHERE account_id = $1`,
			p.AccountID,
		).Scan(&balance)
		if err != nil {
			return nil, fmt.Errorf("postgres: transact %s: balance %s: %w", spec.Kind, p.AccountID, err)
		}
		balanceAfter := balance + p.Amount
		entry := &wallet.Entry{
			TransactionID: txn.ID,
			AccountID:     p.AccountID,
			Amount:        p.Amount,
			BalanceAfter:  balanceAfter,
			Kind:          spec.Kind,
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
			return nil, fmt.Errorf("postgres: transact %s: entry %s: %w", spec.Kind, p.AccountID, err)
		}
		entry.CreatedAt = entry.CreatedAt.UTC()
		txn.Entries = append(txn.Entries, entry)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: transact %s: commit: %w", spec.Kind, err)
	}
	return txn, nil
}

// lockAccount takes the account's row lock, which is what serializes the
// entry sums that follow it. User accounts are implicit — created by their
// first posting, and the conflicting upsert locks the row just as an update
// would; system accounts are seeded by migration, so posting to a missing one
// is a wiring bug, not a case to auto-heal.
func lockAccount(ctx context.Context, tx pgx.Tx, accountID string) error {
	if userID, currency, ok := wallet.ParseUserAccountID(accountID); ok {
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

func (l *Ledger) Entries(ctx context.Context, accountID string, q wallet.EntriesQuery) ([]*wallet.Entry, error) {
	sql := `SELECT e.id, e.txn_id, e.account_id, e.amount, e.balance_after, e.created_at,
			t.kind, t.ref, t.metadata
		FROM entries e
		JOIN transactions t ON t.id = e.txn_id
		WHERE e.account_id = $1`
	args := []any{accountID}
	if q.Kind != "" {
		args = append(args, string(q.Kind))
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
		return nil, fmt.Errorf("postgres: entries %s: %w", accountID, err)
	}
	defer rows.Close()

	var entries []*wallet.Entry
	for rows.Next() {
		var (
			entry     wallet.Entry
			kind      string
			ref       pgtype.Text
			metadata  []byte
			createdAt time.Time
		)
		if err := rows.Scan(&entry.ID, &entry.TransactionID, &entry.AccountID, &entry.Amount, &entry.BalanceAfter, &createdAt, &kind, &ref, &metadata); err != nil {
			return nil, fmt.Errorf("postgres: entries %s: scan: %w", accountID, err)
		}
		entry.Kind = wallet.Kind(kind)
		entry.Ref = ref.String
		entry.Metadata = metadata
		entry.CreatedAt = createdAt.UTC()
		entries = append(entries, &entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: entries %s: %w", accountID, err)
	}
	return entries, nil
}

func (l *Ledger) validateSchema(ctx context.Context) error {
	for _, table := range []string{"accounts", "transactions", "entries"} {
		rows, err := l.db.Query(ctx, `SELECT * FROM `+pgx.Identifier{table}.Sanitize()+` LIMIT 0`)
		if err != nil {
			return fmt.Errorf("postgres: schema validation for table %q: %w", table, err)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("postgres: schema validation for table %q: %w", table, err)
		}
	}
	return nil
}
