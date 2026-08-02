package agent

// Charging is the one place where token counts turn into money, so its
// arithmetic, the postings it writes, and the receipt it carries are all
// pinned here. The ledger below is a real in-memory implementation of the two
// ports this package declares, not a double: it enforces the
// balanced-postings rule and answers reads from what it stored, so a charge is
// observed by its effect rather than by a recorded expectation. It is also why
// those ports exist at all — the wallet's own Ledger is a concrete PostgreSQL
// struct, so without them this arithmetic could only be tested against a
// database.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/avagenc/chat/wallet"
)

type memLedger struct {
	entries []*wallet.Entry
	err     error
	ctxErr  error
}

func (m *memLedger) Balance(ctx context.Context, accountID string) (int64, error) {
	var balance int64
	for _, e := range m.entries {
		if e.AccountID == accountID {
			balance += e.Amount
		}
	}
	return balance, nil
}

func (m *memLedger) Transact(ctx context.Context, spec wallet.Spec) (*wallet.Transaction, error) {
	if m.err != nil {
		return nil, m.err
	}
	var sum int64
	for _, p := range spec.Postings {
		sum += p.Amount
	}
	if len(spec.Postings) < 2 || sum != 0 {
		return nil, fmt.Errorf("wallet: spec of %d postings sums to %d, want at least 2 summing to 0", len(spec.Postings), sum)
	}
	m.ctxErr = ctx.Err()
	txn := &wallet.Transaction{
		ID:        fmt.Sprintf("txn-%d", len(m.entries)),
		Type:      spec.Type,
		Ref:       spec.Ref,
		Metadata:  spec.Metadata,
		CreatedAt: time.Now(),
	}
	for _, p := range spec.Postings {
		balance, _ := m.Balance(ctx, p.AccountID)
		entry := &wallet.Entry{
			TransactionID: txn.ID,
			AccountID:     p.AccountID,
			Amount:        p.Amount,
			BalanceAfter:  balance + p.Amount,
			Type:          spec.Type,
			Ref:           spec.Ref,
			Metadata:      spec.Metadata,
			CreatedAt:     txn.CreatedAt,
		}
		m.entries = append(m.entries, entry)
		txn.Entries = append(txn.Entries, entry)
	}
	return txn, nil
}

func (m *memLedger) Entries(ctx context.Context, accountID string, q wallet.EntriesQuery) ([]*wallet.Entry, error) {
	var out []*wallet.Entry
	for _, e := range m.entries {
		if e.AccountID != accountID {
			continue
		}
		if q.Type != "" && e.Type != q.Type {
			continue
		}
		if !q.Since.IsZero() && e.CreatedAt.Before(q.Since) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

var (
	_ Ledger       = (*memLedger)(nil)
	_ LedgerReader = (*memLedger)(nil)
)

// charges returns what was booked against the user's own account, which is
// where a run charge lands. Amounts stay as the ledger signed them.
func (m *memLedger) charges(userID string) []*wallet.Entry {
	out, _ := m.Entries(context.Background(), wallet.UserAccountID(userID, wallet.IDR), wallet.EntriesQuery{})
	return out
}

// testPrice mirrors the shape of the rates wired in main: rupiah per million
// tokens, so one token costs exactly its rate in micro-rupiah.
var testPrice = Price{InputPerMTok: 10_000, CachedPerMTok: 2_500, OutputPerMTok: 42_000}

func TestBillerCharge(t *testing.T) {
	tests := []struct {
		name       string
		usage      Usage
		wantMicros int64
		wantInput  int64
	}{
		{
			// PromptTokenCount already includes the cached tokens, so the
			// cached share is billed at its own rate and only the remainder
			// counts as input. Double-counting here would overcharge silently.
			name: "cached share billed separately",
			usage: Usage{
				Prompt: 1_000, Cached: 400, Candidates: 200, Thoughts: 50, Total: 1_250,
			},
			wantInput:  600,
			wantMicros: 600*10_000 + 400*2_500 + 250*42_000,
		},
		{
			name: "tool use prompt counts as input",
			usage: Usage{
				Prompt: 1_000, ToolUsePrompt: 300, Cached: 0, Candidates: 100, Total: 1_400,
			},
			wantInput:  1_300,
			wantMicros: 1_300*10_000 + 100*42_000,
		},
		{
			// Defensive clamp: a provider reporting more cached than prompt
			// tokens must never produce a negative (crediting) charge.
			name: "cached exceeds prompt clamps to zero input",
			usage: Usage{
				Prompt: 100, Cached: 500, Candidates: 10, Total: 510,
			},
			wantInput:  0,
			wantMicros: 500*2_500 + 10*42_000,
		},
		{
			name: "thinking tokens billed as output",
			usage: Usage{
				Prompt: 0, Candidates: 0, Thoughts: 100, Total: 100,
			},
			wantInput:  0,
			wantMicros: 100 * 42_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := &memLedger{}
			biller := NewBiller(ledger, testPrice)

			if err := biller.Charge(context.Background(), "user-1", Run{Agent: "ava", Session: "chat-user-1", Trigger: "human"}, tt.usage); err != nil {
				t.Fatalf("Charge() error = %v", err)
			}
			charges := ledger.charges("user-1")
			if len(charges) != 1 {
				t.Fatalf("Charge() recorded %d charges, want 1", len(charges))
			}
			if got := -charges[0].Amount; got != tt.wantMicros {
				t.Errorf("charge = %d micros, want %d", got, tt.wantMicros)
			}

			var receipt Receipt
			if err := json.Unmarshal(charges[0].Metadata, &receipt); err != nil {
				t.Fatalf("unmarshal receipt: %v", err)
			}
			if receipt.Tokens.Input != tt.wantInput {
				t.Errorf("receipt input tokens = %d, want %d", receipt.Tokens.Input, tt.wantInput)
			}
			if receipt.Tokens.Total != tt.usage.Total {
				t.Errorf("receipt total tokens = %d, want %d", receipt.Tokens.Total, tt.usage.Total)
			}
			if receipt.Price != testPrice {
				t.Errorf("receipt price = %+v, want %+v", receipt.Price, testPrice)
			}
		})
	}
}

// A run that consumed nothing is not a free run to record — it is not a run.
func TestBillerChargeSkipsEmptyUsage(t *testing.T) {
	ledger := &memLedger{}
	biller := NewBiller(ledger, testPrice)

	if err := biller.Charge(context.Background(), "user-1", Run{Agent: "zee"}, Usage{}); err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if len(ledger.entries) != 0 {
		t.Errorf("Charge() recorded %d entries for empty usage, want 0", len(ledger.entries))
	}
}

// The charge doubles as the usage log, so one that rounds to nothing still has
// to be recorded — a floor of one micro keeps the row.
func TestBillerChargeFloorsAtOneMicro(t *testing.T) {
	ledger := &memLedger{}
	biller := NewBiller(ledger, Price{InputPerMTok: 0, CachedPerMTok: 0, OutputPerMTok: 0})

	if err := biller.Charge(context.Background(), "user-1", Run{Agent: "zee"}, Usage{Prompt: 10, Total: 10}); err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	charges := ledger.charges("user-1")
	if len(charges) != 1 {
		t.Fatalf("Charge() recorded %d charges, want 1", len(charges))
	}
	if got := -charges[0].Amount; got != 1 {
		t.Errorf("charge = %d micros, want 1", got)
	}
}

// The receipt is what the usage endpoint reads back, so it has to carry the
// whole provenance of the charge.
func TestBillerChargeRecordsRunContext(t *testing.T) {
	ledger := &memLedger{}
	biller := NewBiller(ledger, testPrice)
	run := Run{Agent: "zee", Session: "chat-user-1", Trigger: "ava"}

	if err := biller.Charge(context.Background(), "user-1", run, Usage{Prompt: 100, Total: 100, ModelVersion: "gemini-3.5-flash"}); err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(ledger.charges("user-1")[0].Metadata, &receipt); err != nil {
		t.Fatalf("unmarshal receipt: %v", err)
	}
	if receipt.Agent != run.Agent || receipt.Session != run.Session || receipt.Trigger != run.Trigger {
		t.Errorf("receipt run context = %+v, want %+v", receipt, run)
	}
	if receipt.Model != "gemini-3.5-flash" {
		t.Errorf("receipt model = %q, want %q", receipt.Model, "gemini-3.5-flash")
	}
}

// The tokens were spent whether or not the client is still waiting, so the
// charge must outlive a cancelled request context.
func TestBillerChargeSurvivesCancellation(t *testing.T) {
	ledger := &memLedger{}
	biller := NewBiller(ledger, testPrice)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := biller.Charge(ctx, "user-1", Run{Agent: "ava"}, Usage{Prompt: 100, Total: 100}); err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if got := len(ledger.charges("user-1")); got != 1 {
		t.Fatalf("Charge() recorded %d charges, want 1", got)
	}
	if ledger.ctxErr != nil {
		t.Errorf("ledger saw ctx.Err() = %v, want nil", ledger.ctxErr)
	}
}

// A charge is money moving, so it is booked as a balanced pair: the user is
// debited exactly what revenue is credited, under a kind the wallet stores
// but never interprets.
func TestBillerChargePostsBalancedPair(t *testing.T) {
	ledger := &memLedger{}
	biller := NewBiller(ledger, testPrice)

	if err := biller.Charge(context.Background(), "user-1", Run{Agent: "ava"}, Usage{Prompt: 1_000, Total: 1_000}); err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if len(ledger.entries) != 2 {
		t.Fatalf("Charge() wrote %d entries, want 2", len(ledger.entries))
	}
	want := int64(1_000 * 10_000)
	debit, err := ledger.Balance(context.Background(), wallet.UserAccountID("user-1", wallet.IDR))
	if err != nil {
		t.Fatalf("Balance() error = %v", err)
	}
	if debit != -want {
		t.Errorf("user account = %d micros, want %d", debit, -want)
	}
	credit, err := ledger.Balance(context.Background(), wallet.RevenueAccountID(wallet.IDR))
	if err != nil {
		t.Fatalf("Balance() error = %v", err)
	}
	if credit != want {
		t.Errorf("revenue account = %d micros, want %d", credit, want)
	}
	for _, e := range ledger.entries {
		if e.Type != TxAgentRun {
			t.Errorf("entry kind = %q, want %q", e.Type, TxAgentRun)
		}
		if len(e.Metadata) == 0 {
			t.Errorf("entry on %s carries no receipt", e.AccountID)
		}
	}
}

// A tool-looping run emits several model events, and the drain loop folds
// every one of them — including the ones that carry no usage at all.
func TestUsageAdd(t *testing.T) {
	var usage Usage
	usage.Add(nil)
	usage.Add(&adksession.Event{})
	usage.Add(&adksession.Event{LLMResponse: model.LLMResponse{
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 1_000, CachedContentTokenCount: 200,
			CandidatesTokenCount: 50, TotalTokenCount: 1_050,
		},
		ModelVersion: "gemini-3.5-flash",
	}})
	usage.Add(&adksession.Event{LLMResponse: model.LLMResponse{
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 2_000, ToolUsePromptTokenCount: 300,
			ThoughtsTokenCount: 25, TotalTokenCount: 2_325,
		},
	}})

	want := Usage{
		Prompt: 3_000, Cached: 200, ToolUsePrompt: 300,
		Candidates: 50, Thoughts: 25, Total: 3_375,
		ModelVersion: "gemini-3.5-flash",
	}
	if usage != want {
		t.Errorf("Usage = %+v, want %+v", usage, want)
	}
}

func TestBillerChargeWrapsLedgerError(t *testing.T) {
	wantErr := errors.New("ledger down")
	ledger := &memLedger{err: wantErr}
	biller := NewBiller(ledger, testPrice)

	err := biller.Charge(context.Background(), "user-1", Run{Agent: "ava"}, Usage{Prompt: 100, Total: 100})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Charge() error = %v, want %v", err, wantErr)
	}
}
