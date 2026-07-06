// Package gworkspace is the linking slice for Google Workspace: the
// user-facing surface to connect and disconnect a Google Workspace account
// (mint consent URL → connect → disconnect). Agents that consume the resulting
// tokens (rafal) are deliberately not this package's concern — linking only
// manages the stored grant. Sibling integrations each get their own package
// under internal/link; only code discovered to be identical across them
// (the HMAC state parameter) lives in the shared link root.
package gworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/avagenc/chat/internal/link"
	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
	gworkspacesdk "go.naturallyfunny.dev/gworkspace"
	"golang.org/x/oauth2"
)

// Connector is the slice of *gworkspace.Client this handler needs: mint a
// consent URL, trade the callback code for a stored refresh token, report
// whether one is stored, and forget it again.
type Connector interface {
	AuthURL(state string) string
	Connect(ctx context.Context, owner, code string) error
	Connected(ctx context.Context, owner string) (bool, error)
	Disconnect(ctx context.Context, owner string) error
}

// Handler is the HTTP glue for a user connecting their Google Workspace
// account. stateSecret signs the OAuth state parameter, binding the consent
// URL to the user it was minted for (see linking.SignState).
type Handler struct {
	connector   Connector
	stateSecret []byte
}

func NewHandler(connector Connector, stateSecret []byte) *Handler {
	return &Handler{connector: connector, stateSecret: stateSecret}
}

// integration names this slice inside the OAuth state parameter: it
// domain-separates the shared state secret, and the frontend callback page
// reads it to route the flow back to this integration's connect endpoint.
const integration = "gworkspace"

// HandleAuthURL mints the Google consent URL the frontend must send the user
// to. Google redirects back to the shared frontend callback page with
// ?code=&state=, which the frontend forwards verbatim to HandleConnect.
func (h *Handler) HandleAuthURL(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}
	state := link.SignState(h.stateSecret, integration, userID, time.Now().Add(link.StateTTL))
	apihttp.WriteJSON(w, http.StatusOK, struct {
		URL string `json:"url"`
	}{h.connector.AuthURL(state)})
}

// HandleStatus reports whether the caller has a Google Workspace account
// connected, so the frontend can render "Terhubung" vs "Hubungkan". Storage
// truth only — it does not probe the grant at Google.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}
	connected, err := h.connector.Connected(r.Context(), userID)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, struct {
		Connected bool `json:"connected"`
	}{connected})
}

// HandleConnect completes the OAuth flow: it verifies the state belongs to
// the caller, trades the code for a refresh token, and persists it.
func (h *Handler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	var body struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" || body.State == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "code and state required"})
		return
	}
	if !link.VerifyState(h.stateSecret, body.State, integration, userID, time.Now()) {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "invalid or expired state"})
		return
	}

	err = h.connector.Connect(r.Context(), userID, body.Code)
	if errors.Is(err, gworkspacesdk.ErrMissingScopes) {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "google did not grant all requested permissions — restart the flow and keep every permission selected"})
		return
	}
	// Google refusing the code exchange (expired, reused, or wrong redirect
	// URI) is the caller's error, not an upstream outage.
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "google rejected the authorization code"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleDisconnect removes the caller's stored Google Workspace refresh
// token. The grant itself stays listed on the user's Google Account until
// they revoke it there.
func (h *Handler) HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	err = h.connector.Disconnect(r.Context(), userID)
	if errors.Is(err, gworkspacesdk.ErrNotConnected) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "not connected"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
