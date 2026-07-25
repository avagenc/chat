package wallet

// Charging is the one place where token counts turn into money, so its
// arithmetic and its posting shape are pinned here. The Ledger below is a real
// in-memory implementation of the port — the same contract postgres implements,
// exercised for its actual behaviour — not a mock with recorded expectations.
// The postgres adapter's own guarantees (atomicity, locking, idempotency) are
// covered by postgres/ledger_test.go against a real database.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// memLedger is an in-memory wallet.Ledger. It keeps the balanced-postings rule
// the port promises, so a spec that would be rejected by postgres is rejected
// here too.
type memLedger struct {
	transactions []Spec
	refs         map[string]bool
	err          error
	ctxErr       error
}

func newMemLedger() *memLedger { return &memLedger{refs: map[string]bool{}} }

func (m *memLedger) Balance(ctx context.Context, accountID string) (int64, error) {
	var balance int64
	for _, spec := range m.transactions {
		for _, p := range spec.Postings {
			if p.AccountID == accountID {
				balance += p.Amount
			}
		}
	}
	return balance, nil
}

func (m *memLedger) Transact(ctx context.Context, spec Spec) (*Transaction, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.ctxErr = ctx.Err()
	var sum int64
	for _, p := range spec.Postings {
		sum += p.Amount
	}
	if sum != 0 {
		return nil, errors.New("unbalanced postings")
	}
	if spec.Ref != "" {
		if m.refs[spec.Ref] {
			return nil, ErrDuplicateRef
		}
		m.refs[spec.Ref] = true
	}
	m.transactions = append(m.transactions, spec)
	return &Transaction{Kind: spec.Kind, Ref: spec.Ref, Metadata: spec.Metadata}, nil
}

func (m *memLedger) Entries(ctx context.Context, accountID string, q EntriesQuery) ([]*Entry, error) {
	return nil, nil
}

var _ Ledger = (*memLedger)(nil)

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
			ledger := newMemLedger()
			biller := NewBiller(ledger, testPrice)

			if err := biller.Charge(context.Background(), "user-1", Run{Agent: "ava", Session: "chat-user-1", Trigger: "human"}, tt.usage); err != nil {
				t.Fatalf("Charge() error = %v", err)
			}
			if len(ledger.transactions) != 1 {
				t.Fatalf("Charge() wrote %d transactions, want 1", len(ledger.transactions))
			}
			spec := ledger.transactions[0]
			if spec.Kind != KindAgentRun {
				t.Errorf("kind = %q, want %q", spec.Kind, KindAgentRun)
			}
			if len(spec.Postings) != 2 {
				t.Fatalf("got %d postings, want 2", len(spec.Postings))
			}
			user, revenue := spec.Postings[0], spec.Postings[1]
			if user.AccountID != UserAccountID("user-1") {
				t.Errorf("debit account = %q, want %q", user.AccountID, UserAccountID("user-1"))
			}
			if revenue.AccountID != AccountRevenue {
				t.Errorf("credit account = %q, want %q", revenue.AccountID, AccountRevenue)
			}
			if user.Amount != -tt.wantMicros {
				t.Errorf("user posting = %d micros, want %d", user.Amount, -tt.wantMicros)
			}
			if user.Amount+revenue.Amount != 0 {
				t.Errorf("postings sum to %d, want 0", user.Amount+revenue.Amount)
			}

			var receipt Receipt
			if err := json.Unmarshal(spec.Metadata, &receipt); err != nil {
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
	ledger := newMemLedger()
	biller := NewBiller(ledger, testPrice)

	if err := biller.Charge(context.Background(), "user-1", Run{Agent: "zee"}, Usage{}); err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if len(ledger.transactions) != 0 {
		t.Errorf("Charge() wrote %d transactions for empty usage, want 0", len(ledger.transactions))
	}
}

// The transaction doubles as the usage log, so a charge that rounds to nothing
// still has to be recorded — a floor of one micro keeps the row.
func TestBillerChargeFloorsAtOneMicro(t *testing.T) {
	ledger := newMemLedger()
	biller := NewBiller(ledger, Price{InputPerMTok: 0, CachedPerMTok: 0, OutputPerMTok: 0})

	if err := biller.Charge(context.Background(), "user-1", Run{Agent: "zee"}, Usage{Prompt: 10, Total: 10}); err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if len(ledger.transactions) != 1 {
		t.Fatalf("Charge() wrote %d transactions, want 1", len(ledger.transactions))
	}
	if got := ledger.transactions[0].Postings[0].Amount; got != -1 {
		t.Errorf("user posting = %d micros, want -1", got)
	}
}

// The run's metadata is what the usage endpoint reads back, so it has to carry
// the whole provenance of the charge.
func TestBillerChargeRecordsRunContext(t *testing.T) {
	ledger := newMemLedger()
	biller := NewBiller(ledger, testPrice)
	run := Run{Agent: "zee", Session: "chat-user-1", Trigger: "ava"}

	if err := biller.Charge(context.Background(), "user-1", run, Usage{Prompt: 100, Total: 100, ModelVersion: "gemini-3.5-flash"}); err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(ledger.transactions[0].Metadata, &receipt); err != nil {
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
// debit must outlive a cancelled request context.
func TestBillerChargeSurvivesCancellation(t *testing.T) {
	ledger := newMemLedger()
	biller := NewBiller(ledger, testPrice)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := biller.Charge(ctx, "user-1", Run{Agent: "ava"}, Usage{Prompt: 100, Total: 100}); err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if len(ledger.transactions) != 1 {
		t.Fatalf("Charge() wrote %d transactions, want 1", len(ledger.transactions))
	}
	if ledger.ctxErr != nil {
		t.Errorf("ledger saw ctx.Err() = %v, want nil", ledger.ctxErr)
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
	ledger := newMemLedger()
	ledger.err = wantErr
	biller := NewBiller(ledger, testPrice)

	err := biller.Charge(context.Background(), "user-1", Run{Agent: "ava"}, Usage{Prompt: 100, Total: 100})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Charge() error = %v, want %v", err, wantErr)
	}
}
