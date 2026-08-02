package agent

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	apihttp "go.naturallyfunny.dev/api/http"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"

	"github.com/avagenc/chat/wallet"
)

// microsPerRupiah converts ledger micro-rupiah to display rupiah.
const microsPerRupiah = 1_000_000

// LedgerReader is the half of a wallet ledger that reporting usage needs:
// reading entries back. Declared apart from Ledger in biller.go so that the
// endpoint which only reports cannot record one.
type LedgerReader interface {
	Entries(ctx context.Context, accountID string, q wallet.EntriesQuery) ([]*wallet.Entry, error)
}

// UsageHandler serves what the agents cost the user today. It reads the
// receipts Biller writes, which is why it sits in the same package: the two
// sides of that JSON shape cannot be allowed to drift apart.
type UsageHandler struct {
	ledger LedgerReader
}

func NewUsageHandler(l LedgerReader) *UsageHandler {
	return &UsageHandler{ledger: l}
}

func (h *UsageHandler) HandleToday(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing user identity"})
		return
	}
	tz, err := apitime.ZoneFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "timezone required"})
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "invalid timezone"})
		return
	}
	now := time.Now().In(loc)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	entries, err := h.ledger.Entries(r.Context(), wallet.UserAccountID(userID, chargeCurrency), wallet.EntriesQuery{
		Type:  TxAgentRun,
		Since: startOfDay,
	})
	if err != nil {
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to read usage"})
		return
	}
	var tokens, costMicros int64
	for _, entry := range entries {
		// Cost comes from the recorded amount, not the receipt, so it stays
		// right even if the receipt shape drifts. The ledger is
		// credit-positive, so a charge sits negative on the user's account;
		// flip it, because what is being reported is what a run cost.
		costMicros += -entry.Amount
		var receipt Receipt
		if err := json.Unmarshal(entry.Metadata, &receipt); err != nil {
			log.Printf("error: unmarshal receipt of run charge for user %s: %v", userID, err)
			continue
		}
		tokens += receipt.Tokens.Total
	}
	apihttp.WriteJSON(w, http.StatusOK, struct {
		Tokens     int64 `json:"tokens"`
		Cost       int64 `json:"cost"`
		CostMicros int64 `json:"cost_micros"`
	}{tokens, costMicros / microsPerRupiah, costMicros})
}
