// Package linking holds the shared code real integrations turned out to need.
// Each integration lives in its own subpackage (gworkspace, spotify); this
// root package only grows when the same code is discovered in more than one
// of them — currently the HMAC-signed OAuth state parameter, which every
// SPA-callback OAuth flow here mints and verifies identically.
package linking

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// StateTTL bounds how long a minted consent URL can be completed. Long enough
// for a human to read the provider's consent screen, short enough that a
// leaked state goes stale quickly.
const StateTTL = 15 * time.Minute

// SignState builds the OAuth state parameter: `<unix-expiry>.<base64url mac>`
// with mac = HMAC-SHA256(owner NUL expiry). Binding the owner into the mac —
// verified against the caller's JWT identity on connect — stops the classic
// OAuth CSRF where a victim is tricked into completing the flow with an
// attacker's code, and needs no server-side state store.
func SignState(secret []byte, owner string, expiry time.Time) string {
	exp := strconv.FormatInt(expiry.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(owner + "\x00" + exp))
	return exp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyState checks that state was minted by SignState for this owner and
// has not expired.
func VerifyState(secret []byte, state, owner string, now time.Time) bool {
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
