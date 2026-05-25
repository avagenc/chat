package agent

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	apihttp "go.naturallyfunny.dev/api/http"
	"go.naturallyfunny.dev/api/session"
	apitime "go.naturallyfunny.dev/api/time"
	"go.naturallyfunny.dev/api/user"
)

type Handler struct {
	targetURL *url.URL
	proxy     *httputil.ReverseProxy
}

func NewHandler(targetURL string, base http.RoundTripper) (*Handler, error) {
	target, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("parse target URL: %w", err)
	}

	transport := &apihttp.Transport{
		Base: base,
		Propagators: []apihttp.Propagator{
			apihttp.WithHeader(user.ContextKey, "user-id"),
			apihttp.WithHeader(apitime.ContextKey, "time-zone"),
			apihttp.WithHeader(session.ContextKey, "session-id"),
		},
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport

	return &Handler{
		targetURL: target,
		proxy:     proxy,
	}, nil
}

func (h *Handler) Proxy(w http.ResponseWriter, r *http.Request) {
	r.Host = h.targetURL.Host
	h.proxy.ServeHTTP(w, r)
}
