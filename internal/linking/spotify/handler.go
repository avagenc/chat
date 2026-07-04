// Package spotify is the linking slice for Spotify: the user-facing surface
// to connect and disconnect a Spotify account (mint consent URL → connect →
// disconnect). The agent that consumes the resulting tokens (yori) is
// deliberately not this package's concern — linking only manages the stored
// grant.
package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/avagenc/chat/internal/linking"
	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
	spotifysdk "go.naturallyfunny.dev/spotify"
	"golang.org/x/oauth2"
)

// Connector is the slice of *spotify.Client this handler needs: mint a
// consent URL, trade the callback code for a stored refresh token, and forget
// that token again.
type Connector interface {
	AuthURL(state string, opts ...spotifysdk.AuthOption) string
	Connect(ctx context.Context, userID, code string, opts ...spotifysdk.AuthOption) error
	Disconnect(ctx context.Context, userID string) error
}

// Handler is the HTTP glue for a user connecting their Spotify account.
// stateSecret signs the OAuth state parameter, binding the consent URL to the
// user it was minted for (see linking.SignState).
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
const integration = "spotify"

// HandleAuthURL mints the Spotify consent URL the frontend must send the user
// to. Spotify redirects back to the shared frontend callback page with
// ?code=&state=, which the frontend forwards verbatim to HandleConnect.
func (h *Handler) HandleAuthURL(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	state := linking.SignState(h.stateSecret, integration, userID, time.Now().Add(linking.StateTTL))
	apihttp.WriteJSON(w, http.StatusOK, struct {
		URL string `json:"url"`
	}{h.connector.AuthURL(state)})
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
	if !linking.VerifyState(h.stateSecret, body.State, integration, userID, time.Now()) {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "invalid or expired state"})
		return
	}

	err = h.connector.Connect(r.Context(), userID, body.Code)
	if errors.Is(err, spotifysdk.ErrMissingScopes) {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "spotify did not grant all requested permissions — restart the flow and keep every permission selected"})
		return
	}
	// Spotify refusing the code exchange (expired, reused, or wrong redirect
	// URI) is the caller's error, not an upstream outage.
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "spotify rejected the authorization code"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleDisconnect removes the caller's stored Spotify refresh token. The
// grant itself stays listed on the user's Spotify account
// (spotify.com/account/apps) until they revoke it there.
func (h *Handler) HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	err = h.connector.Disconnect(r.Context(), userID)
	if errors.Is(err, spotifysdk.ErrNotConnected) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "not connected"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
