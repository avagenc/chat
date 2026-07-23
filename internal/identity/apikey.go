package identity

import (
	"crypto/subtle"
	"net/http"

	apihttp "go.naturallyfunny.dev/api/http"
)

type APIKeyAuthenticator struct {
	key []byte
}

func NewAPIKeyAuthenticator(key string) *APIKeyAuthenticator {
	if key == "" {
		panic("identity: NewAPIKeyAuthenticator needs a non-empty key")
	}
	return &APIKeyAuthenticator{key: []byte(key)}
}

func (a *APIKeyAuthenticator) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("api-key")), a.key) != 1 {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "invalid api key"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
