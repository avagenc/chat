package zep

import (
	"fmt"
	"net/http"
	"strconv"

	zep "github.com/getzep/zep-go/v3"
	"github.com/go-chi/chi/v5"
	apihttp "go.naturallyfunny.dev/api/http"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

func (h *Handler) GetMessages(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "session-id")
	if threadID == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID is required"})
		return
	}

	request, err := threadGetRequest(r)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	messages, err := h.service.GetMessages(r.Context(), threadID, request)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "failed to retrieve messages"})
		return
	}

	apihttp.WriteJSON(w, http.StatusOK, messages)
}

func (h *Handler) ClearMessages(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "session-id")
	if threadID == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID is required"})
		return
	}

	response, err := h.service.ClearMessages(r.Context(), threadID)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "failed to clear messages"})
		return
	}

	apihttp.WriteJSON(w, http.StatusOK, response)
}

func threadGetRequest(r *http.Request) (*zep.ThreadGetRequest, error) {
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

	return &zep.ThreadGetRequest{
		Limit:  limit,
		Cursor: cursor,
		Lastn:  lastn,
	}, nil
}

func optionalInt(value string) (*int, error) {
	if value == "" {
		return nil, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return nil, err
	}
	if parsed < 0 {
		return nil, fmt.Errorf("must be greater than or equal to 0")
	}

	return &parsed, nil
}

func optionalInt64(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, err
	}
	if parsed < 0 {
		return nil, fmt.Errorf("must be greater than or equal to 0")
	}

	return &parsed, nil
}
