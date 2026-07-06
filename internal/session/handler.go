package session

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	apihttp "go.naturallyfunny.dev/api/http"
)

// Handler is the HTTP glue for episodic memory: it extracts the session ID
// and pagination query, calls the Service, and maps sentinels to status codes.
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

func optionalInt64(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, errors.New("not an integer")
	}
	if parsed < 0 {
		return nil, errors.New("must be non-negative")
	}
	return &parsed, nil
}

func messagesQuery(r *http.Request) (*MessagesQuery, error) {
	query := r.URL.Query()
	limit, err := optionalInt(query.Get("limit"))
	if err != nil {
		return nil, fmt.Errorf("invalid limit: %w", err)
	}
	cursor, err := optionalInt64(query.Get("cursor"))
	if err != nil {
		return nil, fmt.Errorf("invalid cursor: %w", err)
	}
	lastn, err := optionalInt(query.Get("lastn"))
	if err != nil {
		return nil, fmt.Errorf("invalid lastn: %w", err)
	}
	return &MessagesQuery{
		Limit:  limit,
		Cursor: cursor,
		Lastn:  lastn,
	}, nil
}

func (h *Handler) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID required"})
		return
	}
	query, err := messagesQuery(r)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	messages, err := h.service.GetMessages(r.Context(), sessionID, query)
	if errors.Is(err, ErrNotFound) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "session not found"})
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
	apihttp.WriteJSON(w, http.StatusOK, messages)
}

func (h *Handler) HandleClearMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID required"})
		return
	}
	err := h.service.ClearMessages(r.Context(), sessionID)
	if errors.Is(err, ErrNotFound) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "session not found"})
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
