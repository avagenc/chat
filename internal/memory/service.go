package memory

import (
	"context"
	"errors"
	"fmt"

	"go.naturallyfunny.dev/api/user"
)

var (
	ErrForbidden = errors.New("forbidden")
	ErrNotFound  = errors.New("not found")
)

// SessionService is the episodic-memory service. It enforces ownership before
// returning or mutating a session, since a session ID from the URL could belong
// to anyone.
type SessionService struct {
	store SessionStore
}

func NewSessionService(store SessionStore) *SessionService {
	return &SessionService{
		store: store,
	}
}

func (s *SessionService) GetMessages(ctx context.Context, sessionID string, query *MessagesQuery) (*MessageList, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	messages, err := s.store.Get(ctx, sessionID, query)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get session %q: %w", sessionID, err)
	}

	if messages.UserID == nil || *messages.UserID != userID {
		return nil, ErrForbidden
	}

	return messages, nil
}

func (s *SessionService) ClearMessages(ctx context.Context, sessionID string) (*SuccessResponse, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	messages, err := s.store.Get(ctx, sessionID, &MessagesQuery{})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get session %q: %w", sessionID, err)
	}

	if messages.UserID == nil || *messages.UserID != userID {
		return nil, ErrForbidden
	}

	response, err := s.store.Delete(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("delete session %q: %w", sessionID, err)
	}

	return response, nil
}

// KnowledgeService is the semantic-memory service. Operations scope to the user
// ID from context, so no ownership check is needed.
type KnowledgeService struct {
	store KnowledgeStore
}

func NewKnowledgeService(store KnowledgeStore) *KnowledgeService {
	return &KnowledgeService{
		store: store,
	}
}

// Get returns the caller's knowledge graph.
func (s *KnowledgeService) Get(ctx context.Context, nodesQuery, edgesQuery *GraphQuery) (*KnowledgeGraph, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	nodes, err := s.store.Nodes(ctx, userID, nodesQuery)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get nodes for user %q: %w", userID, err)
	}

	edges, err := s.store.Edges(ctx, userID, edgesQuery)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get edges for user %q: %w", userID, err)
	}

	return &KnowledgeGraph{
		Nodes: nodes,
		Edges: edges,
	}, nil
}

// Delete wipes the caller's entire memory. This removes every session too — the
// behaviour is intentional (see CLAUDE.md).
func (s *KnowledgeService) Delete(ctx context.Context) (*SuccessResponse, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	response, err := s.store.Delete(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("delete memory for user %q: %w", userID, err)
	}

	return response, nil
}
