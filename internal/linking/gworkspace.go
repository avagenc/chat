// Package linking is the user-facing surface for connecting external accounts
// to the platform. Each integration is one vertical slice: gworkspace.go
// carries Google Workspace (mint consent URL → connect → disconnect). Agents
// that consume the resulting tokens (rafal) are deliberately not this
// package's concern — linking only manages the stored grant.
package linking

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
	"go.naturallyfunny.dev/gworkspace"
	"golang.org/x/oauth2"
)

// GworkspaceConnector is the slice of *gworkspace.Client this handler needs:
// mint a consent URL, trade the callback code for a stored refresh token, and
// forget that token again.
type GworkspaceConnector interface {
	AuthURL(state string) string
	Connect(ctx context.Context, owner, code string) error
	Disconnect(ctx context.Context, owner string) error
}

// GworkspaceHandler is the HTTP glue for a user connecting their Google
// Workspace account. stateSecret signs the OAuth state parameter, binding the
// consent URL to the user it was minted for (see signState).
type GworkspaceHandler struct {
	connector   GworkspaceConnector
	stateSecret []byte
}

func NewGworkspaceHandler(connector GworkspaceConnector, stateSecret []byte) *GworkspaceHandler {
	return &GworkspaceHandler{connector: connector, stateSecret: stateSecret}
}

// stateTTL bounds how long a minted consent URL can be completed. Long enough
// for a human to read Google's consent screen, short enough that a leaked
// state goes stale quickly.
const stateTTL = 15 * time.Minute

// signState builds the OAuth state parameter: `<unix-expiry>.<base64url mac>`
// with mac = HMAC-SHA256(owner NUL expiry). Binding the owner into the mac —
// verified against the caller's JWT identity on connect — stops the classic
// OAuth CSRF where a victim is tricked into completing the flow with an
// attacker's code, and needs no server-side state store.
func signState(secret []byte, owner string, expiry time.Time) string {
	exp := strconv.FormatInt(expiry.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(owner + "\x00" + exp))
	return exp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyState checks that state was minted by signState for this owner and
// has not expired.
func verifyState(secret []byte, state, owner string, now time.Time) bool {
	exp, macB64, ok := strings.Cut(state, ".")
	if !ok {
		return false
	}
	expiry, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || now.Unix() > expiry {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(macB64)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(owner + "\x00" + exp))
	return hmac.Equal(got, mac.Sum(nil))
}

// HandleAuthURL mints the Google consent URL the frontend must send the user
// to. Google redirects back to the frontend callback page with ?code=&state=,
// which the frontend forwards verbatim to HandleConnect.
func (h *GworkspaceHandler) HandleAuthURL(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	state := signState(h.stateSecret, userID, time.Now().Add(stateTTL))
	apihttp.WriteJSON(w, http.StatusOK, struct {
		URL string `json:"url"`
	}{h.connector.AuthURL(state)})
}

// HandleConnect completes the OAuth flow: it verifies the state belongs to
// the caller, trades the code for a refresh token, and persists it.
func (h *GworkspaceHandler) HandleConnect(w http.ResponseWriter, r *http.Request) {
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
	if !verifyState(h.stateSecret, body.State, userID, time.Now()) {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "invalid or expired state"})
		return
	}

	err = h.connector.Connect(r.Context(), userID, body.Code)
	if errors.Is(err, gworkspace.ErrMissingScopes) {
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
func (h *GworkspaceHandler) HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	err = h.connector.Disconnect(r.Context(), userID)
	if errors.Is(err, gworkspace.ErrNotConnected) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "not connected"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
