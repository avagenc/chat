package identity

import (
	"net/http"

	"github.com/redis/go-redis/v9"
	apihttp "go.naturallyfunny.dev/api/http"
	"go.naturallyfunny.dev/api/user"
)

type PaymentGuard struct {
	redisClient *redis.Client
}

func NewPaymentGuard(redisClient *redis.Client) *PaymentGuard {
	return &PaymentGuard{redisClient: redisClient}
}

func (g *PaymentGuard) DenyBlocked(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := user.IDFromContext(r.Context())
		if err != nil {
			apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "user identity missing"})
			return
		}
		blocked, err := g.redisClient.SIsMember(r.Context(), "users:blocked:payment", userID).Result()
		if err != nil {
			apihttp.WriteProblem(w, http.StatusInternalServerError, map[string]any{"detail": "system integrity check failed"})
			return
		}
		if blocked {
			apihttp.WriteProblem(w, http.StatusPaymentRequired, map[string]any{"detail": "account suspended due to outstanding payment"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
