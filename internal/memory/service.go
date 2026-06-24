package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/avagenc/chat/memory"
	"go.naturallyfunny.dev/api/user"
)

// ErrForbidden is the gateway's authorization sentinel: the caller has no
// identity, or the requested session belongs to someone else. It is a gateway
// concern, not a port one, so it lives here rather than in the public memory
// package. (memory.ErrNotFound, the port-level sentinel, comes from the adapter.)
var ErrForbidden = errors.New("forbidden")

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
		if errors.Is(err, memory.ErrNotFound) {
			return nil, memory.ErrNotFound
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
		if errors.Is(err, memory.ErrNotFound) {
			return memory.ErrNotFound
		}
		return fmt.Errorf("get session %q: %w", sessionID, err)
	}

	if messages.UserID == nil || *messages.UserID != userID {
		return ErrForbidden
	}

	if err := s.store.Delete(ctx, sessionID); err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			return memory.ErrNotFound
		}
		return fmt.Errorf("delete session %q: %w", sessionID, err)
	}

	return nil
}

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
		if errors.Is(err, memory.ErrNotFound) {
			return nil, memory.ErrNotFound
		}
		return nil, fmt.Errorf("get nodes for user %q: %w", userID, err)
	}

	edges, err := s.store.Edges(ctx, userID, edgesQuery)
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			return nil, memory.ErrNotFound
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
		if errors.Is(err, memory.ErrNotFound) {
			return memory.ErrNotFound
		}
		return fmt.Errorf("delete memory for user %q: %w", userID, err)
	}

	return nil
}
