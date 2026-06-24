// Package memory is the gateway's memory domain. It spans two members of the
// same family: episodic memory — a user's sessions and their messages (SessionStore)
// — and semantic memory — a user's knowledge graph (KnowledgeStore). Both are
// defined here as our own interfaces and types so the domain stays decoupled from
// any provider; the backing implementation lives in the subpackage
// internal/memory/zep. Swapping providers means swapping that adapter without
// touching these services or handlers.
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
	Delete(ctx context.Context, id string) (*SuccessResponse, error)
}

// KnowledgeStore is the semantic-memory port: a user's knowledge graph.
type KnowledgeStore interface {
	// Nodes returns the entity nodes in a user's knowledge graph.
	Nodes(ctx context.Context, userID string, query *GraphQuery) ([]*Node, error)
	// Edges returns the entity edges in a user's knowledge graph.
	Edges(ctx context.Context, userID string, query *GraphQuery) ([]*Edge, error)
	// Delete removes a user's entire memory, including every session.
	Delete(ctx context.Context, userID string) (*SuccessResponse, error)
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

// GraphQuery paginates knowledge-graph nodes or edges.
type GraphQuery struct {
	// Limit caps the number of items returned.
	Limit *int
	// UUIDCursor is the UUID of the last item from the previous page.
	UUIDCursor *string
}

// SuccessResponse is the body returned by mutating operations.
type SuccessResponse struct {
	Message *string `json:"message,omitempty"`
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

// KnowledgeGraph is a user's semantic memory: its entity nodes and edges.
type KnowledgeGraph struct {
	Nodes []*Node `json:"nodes"`
	Edges []*Edge `json:"edges"`
}

// Node is an entity node in a knowledge graph.
type Node struct {
	Attributes    map[string]any `json:"attributes,omitempty"`
	CreatedAt     string         `json:"created_at"`
	Labels        []string       `json:"labels,omitempty"`
	Name          string         `json:"name"`
	Relevance     *float64       `json:"relevance,omitempty"`
	Score         *float64       `json:"score,omitempty"`
	SelectionRank *int           `json:"selection_rank,omitempty"`
	Summary       string         `json:"summary"`
	UUID          string         `json:"uuid"`
}

// Edge is an entity edge (a fact connecting two nodes) in a knowledge graph.
type Edge struct {
	Attributes     map[string]any `json:"attributes,omitempty"`
	CreatedAt      string         `json:"created_at"`
	Episodes       []string       `json:"episodes,omitempty"`
	ExpiredAt      *string        `json:"expired_at,omitempty"`
	Fact           string         `json:"fact"`
	InvalidAt      *string        `json:"invalid_at,omitempty"`
	Name           string         `json:"name"`
	Relevance      *float64       `json:"relevance,omitempty"`
	Scope          *string        `json:"scope,omitempty"`
	Score          *float64       `json:"score,omitempty"`
	SelectionRank  *int           `json:"selection_rank,omitempty"`
	SourceNodeUUID string         `json:"source_node_uuid"`
	TargetNodeUUID string         `json:"target_node_uuid"`
	UUID           string         `json:"uuid"`
	ValidAt        *string        `json:"valid_at,omitempty"`
}
