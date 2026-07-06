// Package session is the gateway's episodic memory: a user's chat sessions
// and the messages within them. The Store port, its domain types, and its
// not-found sentinel live beside the Service that consumes them; the Zep
// adapter in the zep subpackage implements the port and translates backend
// errors into the sentinel, which consumers match with errors.Is. HTTP glue
// lives in handler.go.
package session

import (
	"context"
	"errors"
	"fmt"

	"go.naturallyfunny.dev/api/user"
)

// Service is the episodic-memory service. It enforces ownership before
// returning or mutating a session, since a session ID from the URL could
// belong to anyone.
type Service struct {
	store Store
}

// Message is a single message in a session.
type Message struct {
	Content   string         `json:"content"`
	CreatedAt *string        `json:"created_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Name      *string        `json:"name,omitempty"`
	Processed *bool          `json:"processed,omitempty"`
	Role      string         `json:"role"`
	UUID      *string        `json:"uuid,omitempty"`
}

// MessageList is a session's messages plus pagination metadata.
type MessageList struct {
	Messages   []*Message `json:"messages,omitempty"`
	RowCount   *int       `json:"row_count,omitempty"`
	CreatedAt  *string    `json:"created_at,omitempty"`
	TotalCount *int       `json:"total_count,omitempty"`
	UserID     *string    `json:"user_id,omitempty"`
}

// MessagesQuery paginates a session's messages.
type MessagesQuery struct {
	// Limit caps the number of messages returned.
	Limit *int
	// Cursor is the offset-based pagination cursor.
	Cursor *int64
	// Lastn returns the N most recent messages, overriding Limit and Cursor.
	Lastn *int
}

// Store is the episodic-memory port: a user's sessions and the messages
// within them. The Zep adapter in the zep subpackage implements it.
type Store interface {
	// Get returns a session's messages. The MessageList carries UserID so
	// callers can enforce ownership. Returns ErrNotFound when the session is
	// unknown to the backend.
	Get(ctx context.Context, id string, query *MessagesQuery) (*MessageList, error)
	// Delete removes a session and all of its messages.
	Delete(ctx context.Context, id string) error
}

func NewService(store Store) *Service {
	return &Service{
		store: store,
	}
}

// ErrForbidden is the authorization sentinel: the caller has no identity, or
// the requested session belongs to someone else. Unlike ErrNotFound it never
// comes from an adapter — the Service mints it.
var ErrForbidden = errors.New("forbidden")
// ErrNotFound is the sentinel a Store returns when the backend has no such
// session. The Service and Handler match it with errors.Is to map to a 404.
var ErrNotFound = errors.New("session not found")

func (s *Service) GetMessages(ctx context.Context, sessionID string, query *MessagesQuery) (*MessageList, error) {
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

func (s *Service) ClearMessages(ctx context.Context, sessionID string) error {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return ErrForbidden
	}
	messages, err := s.store.Get(ctx, sessionID, &MessagesQuery{})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("get session %q: %w", sessionID, err)
	}
	if messages.UserID == nil || *messages.UserID != userID {
		return ErrForbidden
	}
	if err := s.store.Delete(ctx, sessionID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("delete session %q: %w", sessionID, err)
	}
	return nil
}
