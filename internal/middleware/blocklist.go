package middleware

import (
	"net/http"

	"github.com/avagenc/api-gateway/pkg/api"
	"github.com/redis/go-redis/v9"
)

type Blocklist struct {
	redis *redis.Client
}

func NewBlocklist(redis *redis.Client) *Blocklist {
	return &Blocklist{redis: redis}
}

func (b *Blocklist) DenyBlocked(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := api.GetUserIDFromContext(r.Context())
		if err != nil {
			api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "User Identity Missing", nil))
			return
		}

		blocked, err := b.redis.SIsMember(r.Context(), "users:blocked:payment", userID).Result()
		if err != nil {
			api.Respond(w, http.StatusInternalServerError, api.NewErrorResponse("INTERNAL_ERROR", "System Integrity Check Failed", nil))
			return
		}

		if blocked {
			api.Respond(w, http.StatusPaymentRequired, api.NewErrorResponse("PAYMENT_REQUIRED", "Account suspended due to outstanding payment.", nil))
			return
		}

		next.ServeHTTP(w, r)
	})
}
