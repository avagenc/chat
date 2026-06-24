package memory

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/avagenc/chat/memory"
	apihttp "go.naturallyfunny.dev/api/http"
	"go.naturallyfunny.dev/api/user"
)

// KnowledgeService is the semantic-memory service. Operations scope to the user
// ID from context, so no ownership check is needed.
type KnowledgeService struct {
	store memory.KnowledgeStore
}

func NewKnowledgeService(store memory.KnowledgeStore) *KnowledgeService {
	return &KnowledgeService{
		store: store,
	}
}

// Get returns the caller's knowledge graph.
func (s *KnowledgeService) Get(ctx context.Context, nodesQuery, edgesQuery *memory.GraphQuery) (*memory.KnowledgeGraph, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	nodes, err := s.store.Nodes(ctx, userID, nodesQuery)
	if err != nil {
		if errors.Is(err, memory.ErrKnowledgeNotFound) {
			return nil, memory.ErrKnowledgeNotFound
		}
		return nil, fmt.Errorf("get nodes for user %q: %w", userID, err)
	}

	edges, err := s.store.Edges(ctx, userID, edgesQuery)
	if err != nil {
		if errors.Is(err, memory.ErrKnowledgeNotFound) {
			return nil, memory.ErrKnowledgeNotFound
		}
		return nil, fmt.Errorf("get edges for user %q: %w", userID, err)
	}

	return &memory.KnowledgeGraph{
		Nodes: nodes,
		Edges: edges,
	}, nil
}

// Delete wipes the caller's entire memory. This removes every session too — the
// behaviour is intentional (see CLAUDE.md).
func (s *KnowledgeService) Delete(ctx context.Context) error {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return ErrForbidden
	}

	if err := s.store.Delete(ctx, userID); err != nil {
		if errors.Is(err, memory.ErrKnowledgeNotFound) {
			return memory.ErrKnowledgeNotFound
		}
		return fmt.Errorf("delete memory for user %q: %w", userID, err)
	}

	return nil
}

// --- semantic memory: knowledge graph ---

func (h *Handler) GetKnowledge(w http.ResponseWriter, r *http.Request) {
	query, err := graphQuery(r)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	graph, err := h.knowledge.Get(r.Context(), query, query)
	if errors.Is(err, memory.ErrKnowledgeNotFound) {
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
	err := h.knowledge.Delete(r.Context())
	if errors.Is(err, memory.ErrKnowledgeNotFound) {
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

func graphQuery(r *http.Request) (*memory.GraphQuery, error) {
	query := r.URL.Query()

	limit, err := optionalInt(query.Get("limit"))
	if err != nil {
		return nil, fmt.Errorf("invalid limit: %w", err)
	}

	q := &memory.GraphQuery{Limit: limit}
	if uuidCursor := query.Get("cursor"); uuidCursor != "" {
		q.UUIDCursor = &uuidCursor
	}
	return q, nil
}
