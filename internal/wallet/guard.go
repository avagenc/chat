package wallet

import (
	"net/http"

	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
)

// Guard gates agent routes on wallet balance. Post-paid model: starting a run
// only requires balance > 0; the debit after the run may dip negative.
type Guard struct {
	ledger Ledger
}

func NewGuard(l Ledger) *Guard {
	return &Guard{ledger: l}
}

func (g *Guard) RequireBalance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := apiuser.IDFromContext(r.Context())
		if err != nil {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing user identity"})
			return
		}
		balance, err := g.ledger.Balance(r.Context(), UserAccountID(userID))
		if err != nil {
			apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "balance check failed"})
			return
		}
		if balance <= 0 {
			apihttp.WriteProblem(w, http.StatusPaymentRequired, map[string]any{"detail": "insufficient balance"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
