package middleware

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/avagenc/api-gateway/pkg/api"
	"github.com/golang-jwt/jwt/v5"
)

type Identity struct {
	jwks keyfunc.Keyfunc
}

func NewIdentity(jwksURL string) (*Identity, error) {
	jwks, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS from URL: %w", err)
	}

	return &Identity{jwks: jwks}, nil
}

func (m *Identity) RequireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "Missing Authorization Header", nil))
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "Invalid Token Format", nil))
			return
		}

		parsedToken, err := jwt.Parse(token, m.jwks.Keyfunc)
		if err != nil {
			log.Printf("JWT Parse Error: %v", err)

			if errors.Is(err, jwt.ErrTokenExpired) {
				api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("TOKEN_EXPIRED", "Token has expired", nil))
				return
			}
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "Invalid Token", nil))
			return
		}

		if !parsedToken.Valid {
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "Token is not valid", nil))
			return
		}

		claims, ok := parsedToken.Claims.(jwt.MapClaims)
		if !ok {
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "Invalid Claims", nil))
			return
		}

		sub, err := claims.GetSubject()
		if err != nil || sub == "" {
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "User ID (sub) not found", nil))
			return
		}

		ctx, err := api.NewContextWithUserID(r.Context(), sub)
		if err != nil {
			api.Respond(w, http.StatusInternalServerError, api.NewErrorResponse("INTERNAL_ERROR", "Failed to set user context", nil))
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
