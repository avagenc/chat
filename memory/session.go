// Package memory defines the gateway's memory domain as pure ports and types:
// episodic memory (SessionStore, session.go) and semantic memory (KnowledgeStore,
// knowledge.go). It is provider-agnostic and imports nothing internal, so it can
// be public: internal/zep implements these ports and internal/memory drives them,
// neither importing the other. Each port carries its own not-found sentinel
// (ErrSessionNotFound, ErrKnowledgeNotFound) that an adapter translates its
// backend error into and consumers match with errors.Is.
package memory

import (
	"context"
	"errors"
)

// ErrSessionNotFound is the sentinel a SessionStore returns when the backend has
// no such session. Services and handlers match it with errors.Is to map to a 404.
var ErrSessionNotFound = errors.New("session not found")

// SessionStore is the episodic-memory port: a user's sessions and the messages
// within them.
type SessionStore interface {
	// Get returns a session's messages. The MessageList carries UserID so
	// callers can enforce ownership. Returns ErrSessionNotFound when the session
	// is unknown to the backend.
	Get(ctx context.Context, id string, query *MessagesQuery) (*MessageList, error)
	// Delete removes a session and all of its messages.
	Delete(ctx context.Context, id string) error
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

// MessageList is a session's messages plus pagination metadata.
type MessageList struct {
	Messages   []*Message `json:"messages,omitempty"`
	RowCount   *int       `json:"row_count,omitempty"`
	CreatedAt  *string    `json:"created_at,omitempty"`
	TotalCount *int       `json:"total_count,omitempty"`
	UserID     *string    `json:"user_id,omitempty"`
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
