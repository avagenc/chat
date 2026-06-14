package postera

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	apihttp "go.naturallyfunny.dev/api/http"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) ListUpcoming(w http.ResponseWriter, r *http.Request) {
	entries, err := h.service.ListUpcoming(r.Context())
	if errors.Is(err, ErrForbidden) {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, entries)
}

func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "posterum-id")
	if id == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "posterum ID required"})
		return
	}

	err := h.service.Cancel(r.Context(), id)
	if errors.Is(err, ErrForbidden) {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}
	if errors.Is(err, ErrNotFound) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "posterum not found"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
