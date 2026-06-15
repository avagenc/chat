package postera

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
	"go.naturallyfunny.dev/postera"
)

type Handler struct {
	postarius *postera.Postarius
}

func NewHandler(p *postera.Postarius) *Handler {
	return &Handler{postarius: p}
}

func (h *Handler) ListUpcoming(w http.ResponseWriter, r *http.Request) {
	if _, err := apiuser.IDFromContext(r.Context()); err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	entries, err := h.postarius.ListUpcoming(r.Context())
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

	if _, err := apiuser.IDFromContext(r.Context()); err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	err := h.postarius.Cancel(r.Context(), id)
	if errors.Is(err, postera.ErrNotFound) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "posterum not found"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
