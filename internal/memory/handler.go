package memory

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
	"go.naturallyfunny.dev/postera"
)

// Handler is the HTTP glue for the whole memory domain. It fronts all three
// memory types of the family: episodic (sessions) via SessionService, semantic
// (knowledge graph) via KnowledgeService, and prospective (self-addressed future
// messages) via Postera's Postarius. Postera differs only in that its service
// already exists upstream; it is no less a memory than the other two.
type Handler struct {
	sessions  *SessionService
	knowledge *KnowledgeService
	postarius *postera.Postarius
}

func NewHandler(sessions *SessionService, knowledge *KnowledgeService, postarius *postera.Postarius) *Handler {
	return &Handler{
		sessions:  sessions,
		knowledge: knowledge,
		postarius: postarius,
	}
}

// --- episodic memory: sessions ---

func (h *Handler) GetMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session-id")
	if sessionID == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID required"})
		return
	}

	query, err := messagesQuery(r)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	messages, err := h.sessions.GetMessages(r.Context(), sessionID, query)
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

func (h *Handler) ClearMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session-id")
	if sessionID == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID required"})
		return
	}

	response, err := h.sessions.ClearMessages(r.Context(), sessionID)
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

	apihttp.WriteJSON(w, http.StatusOK, response)
}

// --- semantic memory: knowledge graph ---

func (h *Handler) GetKnowledge(w http.ResponseWriter, r *http.Request) {
	query, err := graphQuery(r)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	graph, err := h.knowledge.Get(r.Context(), query, query)
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

func (h *Handler) DeleteKnowledge(w http.ResponseWriter, r *http.Request) {
	response, err := h.knowledge.Delete(r.Context())
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

	apihttp.WriteJSON(w, http.StatusOK, response)
}

// --- prospective memory: postera ---

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

// --- query helpers ---

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

func graphQuery(r *http.Request) (*GraphQuery, error) {
	query := r.URL.Query()

	limit, err := optionalInt(query.Get("limit"))
	if err != nil {
		return nil, fmt.Errorf("invalid limit: %w", err)
	}

	q := &GraphQuery{Limit: limit}
	if uuidCursor := query.Get("cursor"); uuidCursor != "" {
		q.UUIDCursor = &uuidCursor
	}
	return q, nil
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
