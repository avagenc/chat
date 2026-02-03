package gateway

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/avagenc/api-gateway/pkg/api"
)

type Handler struct {
	targetURL *url.URL
	proxy     *httputil.ReverseProxy
	apiKey    string
}

func NewHandler(target *url.URL, apiKey string) *Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)

	return &Handler{
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

	r.Header.Set("x-user-id", userID)
	r.Header.Set("x-avagenc-api-key", h.apiKey)

	r.Host = h.targetURL.Host

	h.proxy.ServeHTTP(w, r)
}
