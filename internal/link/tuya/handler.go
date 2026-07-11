// Package tuya is the linking slice for Tuya Smart. Unlike gworkspace and
// spotify, Tuya has no public OAuth flow here: an account is linked manually by
// the team (VIP onboarding) straight into the account store. This slice
// therefore exposes status only — the frontend reads it to render "Terhubung"
// vs the VIP call-to-action. The agent that drives Tuya devices (zee) is not
// this package's concern.
package tuya

import (
	"context"
	"errors"
	"net/http"

	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
	tuya "go.naturallyfunny.dev/tuya"
)

// Connector is the slice of the Tuya client this handler needs: resolve the
// owner's linked account, or report that none is linked via
// tuya.ErrAccountNotLinked. *tuya.Client satisfies it.
type Connector interface {
	Account(ctx context.Context, owner string) (tuya.Account, error)
}

// Handler is the HTTP glue reporting whether a user has a Tuya account linked.
type Handler struct {
	connector Connector
}

func NewHandler(connector Connector) *Handler {
	return &Handler{connector: connector}
}

// HandleStatus reports whether the caller has a Tuya account linked, so the
// frontend can render "Terhubung" vs the VIP onboarding prompt. Storage truth
// only — it does not probe Tuya.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}
	_, err = h.connector.Account(r.Context(), userID)
	if errors.Is(err, tuya.ErrAccountNotLinked) {
		apihttp.WriteJSON(w, http.StatusOK, struct {
			Connected bool `json:"connected"`
		}{false})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, struct {
		Connected bool `json:"connected"`
	}{true})
}
