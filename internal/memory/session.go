package memory

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/avagenc/chat/memory"
	"github.com/go-chi/chi/v5"
	apihttp "go.naturallyfunny.dev/api/http"
	"go.naturallyfunny.dev/api/user"
)

// SessionService is the episodic-memory service. It enforces ownership before
// returning or mutating a session, since a session ID from the URL could belong
// to anyone.
type SessionService struct {
	store memory.SessionStore
}

func NewSessionService(store memory.SessionStore) *SessionService {
	return &SessionService{
		store: store,
	}
}

func (s *SessionService) GetMessages(ctx context.Context, sessionID string, query *memory.MessagesQuery) (*memory.MessageList, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	messages, err := s.store.Get(ctx, sessionID, query)
	if err != nil {
		if errors.Is(err, memory.ErrSessionNotFound) {
			return nil, memory.ErrSessionNotFound
		}
		return nil, fmt.Errorf("get session %q: %w", sessionID, err)
	}

	if messages.UserID == nil || *messages.UserID != userID {
		return nil, ErrForbidden
	}

	return messages, nil
}

func (s *SessionService) ClearMessages(ctx context.Context, sessionID string) error {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return ErrForbidden
	}

	messages, err := s.store.Get(ctx, sessionID, &memory.MessagesQuery{})
	if err != nil {
		if errors.Is(err, memory.ErrSessionNotFound) {
			return memory.ErrSessionNotFound
		}
		return fmt.Errorf("get session %q: %w", sessionID, err)
	}

	if messages.UserID == nil || *messages.UserID != userID {
		return ErrForbidden
	}

	if err := s.store.Delete(ctx, sessionID); err != nil {
		if errors.Is(err, memory.ErrSessionNotFound) {
			return memory.ErrSessionNotFound
		}
		return fmt.Errorf("delete session %q: %w", sessionID, err)
	}

	return nil
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
	if errors.Is(err, memory.ErrSessionNotFound) {
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

	err := h.sessions.ClearMessages(r.Context(), sessionID)
	if errors.Is(err, memory.ErrSessionNotFound) {
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

func messagesQuery(r *http.Request) (*memory.MessagesQuery, error) {
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

	return &memory.MessagesQuery{
		Limit:  limit,
		Cursor: cursor,
		Lastn:  lastn,
	}, nil
}
