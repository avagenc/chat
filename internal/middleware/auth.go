package middleware

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/avagenc/gateway/pkg/api"
	"github.com/golang-jwt/jwt/v5"
)

type UserIdentity struct {
	jwks keyfunc.Keyfunc
}

func NewUserIdentity(jwksURL string) (*UserIdentity, error) {
	jwks, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS from URL: %w", err)
	}

	return &UserIdentity{jwks: jwks}, nil
}

func (m *UserIdentity) RequireUserID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "Missing Authorization Header", nil))
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenString == authHeader {
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "Invalid Token Format", nil))
			return
		}

		token, err := jwt.Parse(tokenString, m.jwks.Keyfunc)

		if err != nil {
			log.Printf("JWT Parse Error: %v", err)
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "Invalid Token", nil))
			return
		}

		if !token.Valid {
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "Token is not valid", nil))
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
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
