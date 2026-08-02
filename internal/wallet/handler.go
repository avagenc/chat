package wallet

import (
	"net/http"

	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
)

// microsPerRupiah converts wallet micro-rupiah to display rupiah.
const microsPerRupiah = 1_000_000

// Handler serves the balance the UI consumes. Amounts leave in rounded rupiah
// for display plus authoritative *_micros fields; rupiah rounds down (floor)
// so a user never sees more than they have — which matters when a post-paid
// balance dips negative.
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
	micros, err := h.ledger.Balance(r.Context(), UserAccountID(userID, IDR))
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
