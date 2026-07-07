package wallet

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	apihttp "go.naturallyfunny.dev/api/http"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
)

// microsPerRupiah converts wallet micro-rupiah to display rupiah.
const microsPerRupiah = 1_000_000

// Handler serves the read endpoints the UI consumes. Amounts leave in rounded
// rupiah for display plus authoritative *_micros fields; rupiah rounds down
// (floor) so a user never sees more than they have — which matters when a
// post-paid balance dips negative.
type Handler struct {
	ledger Ledger
}

func NewHandler(l Ledger) *Handler {
	return &Handler{ledger: l}
}

func (h *Handler) HandleBalance(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing user identity"})
		return
	}
	micros, err := h.ledger.Balance(r.Context(), UserAccountID(userID))
	if err != nil {
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to read balance"})
		return
	}
	// Floor, not truncation toward zero: a negative balance must not display
	// rounder than it is.
	rupiah := micros / microsPerRupiah
	if micros%microsPerRupiah != 0 && micros < 0 {
		rupiah--
	}
	apihttp.WriteJSON(w, http.StatusOK, struct {
		Balance       int64 `json:"balance"`
		BalanceMicros int64 `json:"balance_micros"`
	}{rupiah, micros})
}

func (h *Handler) HandleTodayUsage(w http.ResponseWriter, r *http.Request) {
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
	entries, err := h.ledger.Entries(r.Context(), UserAccountID(userID), EntriesQuery{
		Kind:  KindAgentRun,
		Since: startOfDay,
	})
	if err != nil {
		apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to read usage"})
		return
	}
	var tokens, costMicros int64
	for _, entry := range entries {
		// Cost comes from the entry amount, not the receipt, so it stays
		// right even if the receipt shape drifts.
		costMicros += -entry.Amount
		var receipt Receipt
		if err := json.Unmarshal(entry.Metadata, &receipt); err != nil {
			log.Printf("error: unmarshal receipt of wallet entry %s: %v", entry.ID, err)
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
