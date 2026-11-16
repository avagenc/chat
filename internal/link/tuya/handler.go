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

	apihttp "go.trueardian.com/api/http"
	apiuser "go.trueardian.com/api/user"
	"go.trueardian.com/tuya/appaccount"
)

type Connector interface {
	Get(ctx context.Context, owner string) (appaccount.Account, error)
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
	_, err = h.connector.Get(r.Context(), userID)
	if errors.Is(err, appaccount.ErrNotLinked) {
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
