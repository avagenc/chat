package identity

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	apihttp "go.naturallyfunny.dev/api/http"
	"go.naturallyfunny.dev/api/user"
)

type JWTAuthenticator struct {
	jwks keyfunc.Keyfunc
}

func NewJWTAuthenticator(jwksURL string) (*JWTAuthenticator, error) {
	jwks, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("create JWKS from %q: %w", jwksURL, err)
	}
	return &JWTAuthenticator{jwks: jwks}, nil
}

func (j *JWTAuthenticator) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing authorization header"})
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "invalid token format"})
			return
		}

		parsedToken, err := jwt.Parse(token, j.jwks.Keyfunc)
		if err != nil {
			log.Printf("JWT Parse Error: %v", err)
			if errors.Is(err, jwt.ErrTokenExpired) {
				apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "token has expired"})
				return
			}
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "invalid token"})
			return
		}

		if !parsedToken.Valid {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "token is not valid"})
			return
		}

		claims, ok := parsedToken.Claims.(jwt.MapClaims)
		if !ok {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "invalid claims"})
			return
		}

		sub, err := claims.GetSubject()
		if err != nil || sub == "" {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "user ID (sub) not found"})
			return
		}

		ctx, err := user.ContextWithID(r.Context(), sub)
		if err != nil {
			apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "failed to set user context"})
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
