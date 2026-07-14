package knowledge

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	apihttp "go.naturallyfunny.dev/api/http"
)

// Handler is the HTTP glue for semantic memory: it extracts the pagination
// query, calls the Service, and maps sentinels to status codes.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

func optionalInt(value string) (*int, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return nil, errors.New("not an integer")
	}
	if parsed < 0 {
		return nil, errors.New("must be non-negative")
	}
	return &parsed, nil
}

// graphQueries builds one query per list. Nodes and edges are independent
// sequences in the backend, so a shared cursor cannot page both — each list
// gets its own (node_cursor / edge_cursor); the limit applies to each.
func graphQueries(r *http.Request) (*GraphQuery, *GraphQuery, error) {
	query := r.URL.Query()
	limit, err := optionalInt(query.Get("limit"))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid limit: %w", err)
	}
	nodes := &GraphQuery{Limit: limit}
	if cursor := query.Get("node_cursor"); cursor != "" {
		nodes.UUIDCursor = &cursor
	}
	edges := &GraphQuery{Limit: limit}
	if cursor := query.Get("edge_cursor"); cursor != "" {
		edges.UUIDCursor = &cursor
	}
	return nodes, edges, nil
}

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	nodesQuery, edgesQuery, err := graphQueries(r)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	graph, err := h.service.Get(r.Context(), nodesQuery, edgesQuery)
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
