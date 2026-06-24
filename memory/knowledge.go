package memory

import (
	"context"
	"errors"
)

// ErrKnowledgeNotFound is the sentinel a KnowledgeStore returns when the backend
// has no knowledge graph for the user. Services and handlers match it with
// errors.Is to map to a 404.
var ErrKnowledgeNotFound = errors.New("knowledge not found")

// KnowledgeStore is the semantic-memory port: a user's knowledge graph.
type KnowledgeStore interface {
	// Nodes returns the entity nodes in a user's knowledge graph.
	Nodes(ctx context.Context, userID string, query *GraphQuery) ([]*Node, error)
	// Edges returns the entity edges in a user's knowledge graph.
	Edges(ctx context.Context, userID string, query *GraphQuery) ([]*Edge, error)
	// Delete removes a user's entire memory, including every session.
	Delete(ctx context.Context, userID string) error
}

// GraphQuery paginates knowledge-graph nodes or edges.
type GraphQuery struct {
	// Limit caps the number of items returned.
	Limit *int
	// UUIDCursor is the UUID of the last item from the previous page.
	UUIDCursor *string
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
