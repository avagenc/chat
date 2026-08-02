package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/adk/session"

	"github.com/avagenc/chat/wallet"
)

// Usage accumulates Gemini token counts across one run's model-call events.
// Fields are int64 accumulators: genai reports int32 per event and a
// tool-looping run sums several events, so widen before adding.
type Usage struct {
	Prompt        int64 // PromptTokenCount — includes cached tokens
	Cached        int64 // CachedContentTokenCount
	ToolUsePrompt int64 // ToolUsePromptTokenCount
	Candidates    int64 // CandidatesTokenCount — excludes thoughts
	Thoughts      int64 // ThoughtsTokenCount
	Total         int64 // TotalTokenCount
	ModelVersion  string
}

// Add folds one run event into the accumulator. Non-model events carry no
// UsageMetadata and are skipped, so it is safe to call on every event in a
// drain loop.
func (u *Usage) Add(e *session.Event) {
	if e == nil || e.UsageMetadata == nil {
		return
	}
	md := e.UsageMetadata
	u.Prompt += int64(md.PromptTokenCount)
	u.Cached += int64(md.CachedContentTokenCount)
	u.ToolUsePrompt += int64(md.ToolUsePromptTokenCount)
	u.Candidates += int64(md.CandidatesTokenCount)
	u.Thoughts += int64(md.ThoughtsTokenCount)
	u.Total += int64(md.TotalTokenCount)
	if e.ModelVersion != "" {
		u.ModelVersion = e.ModelVersion
	}
}

// Price is the billing rate in rupiah per million tokens. With ledger amounts
// in micro-rupiah the conversion is exact integer arithmetic:
// micros = rate_idr_per_mtok * tokens.
type Price struct {
	InputPerMTok  int64 `json:"input_per_mtok"`  // non-cached prompt tokens, incl. tool-use prompt
	CachedPerMTok int64 `json:"cached_per_mtok"` // cached prompt tokens
	OutputPerMTok int64 `json:"output_per_mtok"` // candidates + thinking tokens
}

// Run is the context of one agent run, recorded in the receipt.
type Run struct {
	Agent   string // "ava", "zee", "rafal"
	Session string
	Trigger string // "human", "ava", "postera"
}

// Receipt is the JSON shape of a run charge's metadata — the usage log.
// usage.go unmarshals it to sum today's token usage; keep in sync.
type Receipt struct {
	Agent   string `json:"agent"`
	Session string `json:"session"`
	Trigger string `json:"trigger"`
	Model   string `json:"model"`
	Tokens  struct {
		Input    int64 `json:"input"`
		Cached   int64 `json:"cached"`
		Output   int64 `json:"output"`
		Thinking int64 `json:"thinking"`
		Total    int64 `json:"total"`
	} `json:"tokens"`
	Price Price `json:"price"` // rate snapshot at charge time
}

// Ledger is the half of a wallet ledger that billing uses: charging writes,
// it never reads. Declared here rather than taken as wallet.Ledger so a
// Biller cannot reach a balance or an entry list even by accident.
type Ledger interface {
	Transact(ctx context.Context, spec wallet.Spec) (*wallet.Transaction, error)
}

// Biller converts a run's accumulated token usage into one recorded charge.
// It lives beside the agents that spend the tokens, not beside the ledger
// that stores the money: the token-to-rupiah rules are this product's, while
// the ledger is meant to serve any of them.
type Biller struct {
	ledger Ledger
	price  Price
}

func NewBiller(ledger Ledger, price Price) *Biller {
	return &Biller{ledger: ledger, price: price}
}

// TxAgentRun is the wallet transaction type a run charge writes. Untyped and
// minted here, not in the wallet: the ledger stores the label and never reads
// it, so what an agent run is is this side's word.
const TxAgentRun = "agent_run"

// chargeCurrency is rupiah because Price is quoted in rupiah per million
// tokens: the rate decides what currency the micros derived from it are in,
// and there is only one rate.
const chargeCurrency = wallet.IDR

// Charge bills the user for one run and records the usage breakdown as the
// charge's receipt. No-op when the run consumed nothing. The write survives
// request cancellation: tokens were already spent, so the charge must land
// even after the client disconnects.
func (b *Biller) Charge(ctx context.Context, userID string, run Run, u Usage) error {
	if u.Total == 0 {
		return nil
	}
	// PromptTokenCount includes cached tokens; bill the cached share at its
	// own rate and the remainder as input.
	input := u.Prompt + u.ToolUsePrompt - u.Cached
	if input < 0 {
		input = 0
	}
	output := u.Candidates + u.Thoughts
	micros := b.price.InputPerMTok*input + b.price.CachedPerMTok*u.Cached + b.price.OutputPerMTok*output
	if micros < 1 {
		// Floor at 1 micro (10^-6 IDR) so the charge is still recorded — it
		// doubles as the usage log.
		micros = 1
	}

	receipt := Receipt{
		Agent:   run.Agent,
		Session: run.Session,
		Trigger: run.Trigger,
		Model:   u.ModelVersion,
		Price:   b.price,
	}
	receipt.Tokens.Input = input
	receipt.Tokens.Cached = u.Cached
	receipt.Tokens.Output = u.Candidates
	receipt.Tokens.Thinking = u.Thoughts
	receipt.Tokens.Total = u.Total
	metadata, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("charge %s run: marshal receipt: %w", run.Agent, err)
	}

	if _, err := b.ledger.Transact(context.WithoutCancel(ctx), wallet.Spec{
		Type:     TxAgentRun,
		Metadata: metadata,
		Postings: []wallet.Posting{
			{AccountID: wallet.UserAccountID(userID, chargeCurrency), Amount: -micros},
			{AccountID: wallet.RevenueAccountID(chargeCurrency), Amount: micros},
		},
	}); err != nil {
		return fmt.Errorf("charge %s run: %w", run.Agent, err)
	}
	return nil
}
