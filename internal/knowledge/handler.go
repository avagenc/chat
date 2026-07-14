package knowledge

import (
	"errors"
	"net/http"

	apihttp "go.naturallyfunny.dev/api/http"
)

// Handler is the HTTP glue for semantic memory: it calls the Service and
// maps sentinels to status codes. GET /knowledge returns the caller's whole
// graph — no pagination surface; the adapter drains the backend's pages.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	graph, err := h.service.Get(r.Context())
	if errors.Is(err, ErrNotFound) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "memory not found"})
		return
	}
	if errors.Is(err, ErrForbidden) {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, graph)
}

func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	err := h.service.Delete(r.Context())
	if errors.Is(err, ErrNotFound) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "memory not found"})
		return
	}
	if errors.Is(err, ErrForbidden) {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
