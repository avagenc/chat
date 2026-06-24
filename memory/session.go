package memory

import "context"

// SessionStore is the episodic-memory port: a user's sessions and the messages
// within them.
type SessionStore interface {
	// Get returns a session's messages. The MessageList carries UserID so
	// callers can enforce ownership. Returns ErrNotFound when the session is
	// unknown to the backend.
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
