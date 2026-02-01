package gateway

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/avagenc/api-gateway/pkg/api"
)

type Service interface {
	IsUserBlocked(ctx context.Context, userID string) (bool, error)
}

type Handler struct {
	svc       Service
	targetURL *url.URL
	proxy     *httputil.ReverseProxy
	apiKey    string
}

func NewHandler(svc Service, target *url.URL, apiKey string) *Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)

	return &Handler{
		svc:       svc,
		targetURL: target,
		proxy:     proxy,
		apiKey:    apiKey,
	}
}

func (h *Handler) Proxy(w http.ResponseWriter, r *http.Request) {
	userID, err := api.GetUserIDFromContext(r.Context())
	if err != nil {
		api.Respond(w, http.StatusUnauthorized, api.NewErrorResponse("UNAUTHORIZED", "User Identity Missing", nil))
		return
	}

	blocked, err := h.svc.IsUserBlocked(r.Context(), userID)
	if err != nil {
		api.Respond(w, http.StatusInternalServerError, api.NewErrorResponse("INTERNAL_ERROR", "System Integrity Check Failed", nil))
		return
	}
	if blocked {
		api.Respond(w, http.StatusPaymentRequired, api.NewErrorResponse("PAYMENT_REQUIRED", "Account suspended due to outstanding payment.", nil))
		return
	}

	r.Header.Set("x-user-id", userID)
	r.Header.Set("x-avagenc-api-key", h.apiKey)

	r.Host = h.targetURL.Host

	h.proxy.ServeHTTP(w, r)
}
