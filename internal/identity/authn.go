package identity

import (
	"log"
	"net/http"
	"strings"

	"firebase.google.com/go/v4/auth"
	apihttp "go.naturallyfunny.dev/api/http"
	"go.naturallyfunny.dev/api/user"
)

type FirebaseAuthenticator struct {
	authClient *auth.Client
}

func NewFirebaseAuthenticator(authClient *auth.Client) *FirebaseAuthenticator {
	return &FirebaseAuthenticator{authClient: authClient}
}

func (f *FirebaseAuthenticator) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing authorization header"})
			return
		}
		idToken := strings.TrimPrefix(authHeader, "Bearer ")
		if idToken == authHeader {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "invalid token format"})
			return
		}
		token, err := f.authClient.VerifyIDToken(r.Context(), idToken)
		if err != nil {
			log.Printf("Firebase ID token verify error: %v", err)
			if auth.IsIDTokenExpired(err) {
				apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "token has expired"})
				return
			}
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "invalid token"})
			return
		}
		ctx, err := user.ContextWithID(r.Context(), token.UID)
		if err != nil {
			apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to set user context"})
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
