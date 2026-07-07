package identity

import (
	"log"
	"net/http"
	"strings"

	apihttp "go.naturallyfunny.dev/api/http"
	"google.golang.org/api/idtoken"
)

// CloudTasksAuthenticator authenticates a request as coming from this service's
// own Cloud Tasks queue. The awaken callback (/ava/awaken) runs an agent and
// bills the user, yet must sit outside Firebase auth because Cloud Tasks — not
// a browser — calls it. Cloud Tasks attaches a Google-signed OIDC token for the
// runtime service account (postera's enqueuer sets it); this middleware verifies
// that token instead of a shared secret. An OIDC token is strictly better than
// an API key here: it is already present, short-lived, asymmetrically signed by
// Google, audience-bound, and nothing to store, rotate, or leak.
//
// Without this, the endpoint trusts the raw user-id header, so anyone on the
// internet could POST it and drain another user's wallet one agent run at a
// time (RequireBalance only blocks once a balance already hit zero).
type CloudTasksAuthenticator struct {
	// audience is the OIDC token's expected aud claim: the exact task target
	// URL Cloud Tasks was configured with (host + /ava/awaken).
	audience string
	// serviceAccountEmail is the runtime SA the token must be issued for.
	serviceAccountEmail string
}

func NewCloudTasksAuthenticator(audience, serviceAccountEmail string) *CloudTasksAuthenticator {
	if audience == "" || serviceAccountEmail == "" {
		panic("identity: NewCloudTasksAuthenticator needs a non-empty audience and service account email")
	}
	return &CloudTasksAuthenticator{audience: audience, serviceAccountEmail: serviceAccountEmail}
}

func (a *CloudTasksAuthenticator) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing bearer token"})
			return
		}
		// Validate checks Google's signature, expiry, and the aud claim against
		// our target URL, so a token minted for a different audience (another
		// service, another endpoint) is rejected.
		payload, err := idtoken.Validate(r.Context(), token, a.audience)
		if err != nil {
			log.Printf("cloud tasks OIDC verify error: %v", err)
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "invalid token"})
			return
		}
		if payload.Issuer != "https://accounts.google.com" && payload.Issuer != "accounts.google.com" {
			log.Printf("cloud tasks OIDC: unexpected issuer %q", payload.Issuer)
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "invalid token"})
			return
		}
		email, _ := payload.Claims["email"].(string)
		verified, _ := payload.Claims["email_verified"].(bool)
		if !verified || email != a.serviceAccountEmail {
			log.Printf("cloud tasks OIDC: token email %q (verified %t) is not the runtime service account", email, verified)
			apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
