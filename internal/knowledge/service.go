// Package knowledge is the gateway's semantic memory: a user's knowledge
// graph. The Store port, its domain types, and its not-found sentinel live
// beside the Service that consumes them; the Zep adapter in the zep
// subpackage implements the port and translates backend errors into the
// sentinel, which consumers match with errors.Is. HTTP glue lives in
// handler.go.
package knowledge

import (
	"context"
	"errors"
	"fmt"

	"go.naturallyfunny.dev/api/user"
)

// Service is the semantic-memory service. Operations scope to the user ID
// from context, so no ownership check is needed.
type Service struct {
	store Store
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

// GraphQuery paginates knowledge-graph nodes or edges.
type GraphQuery struct {
	// Limit caps the number of items returned.
	Limit *int
	// UUIDCursor is the UUID of the last item from the previous page.
	UUIDCursor *string
}

// Store is the semantic-memory port: a user's knowledge graph. The Zep
// adapter in the zep subpackage implements it.
type Store interface {
	// Nodes returns the entity nodes in a user's knowledge graph.
	Nodes(ctx context.Context, userID string, query *GraphQuery) ([]*Node, error)
	// Edges returns the entity edges in a user's knowledge graph.
	Edges(ctx context.Context, userID string, query *GraphQuery) ([]*Edge, error)
	// Delete removes a user's entire memory, including every session.
	Delete(ctx context.Context, userID string) error
}

func NewService(store Store) *Service {
	return &Service{
		store: store,
	}
}

// ErrNotFound is the sentinel a Store returns when the backend has no
// knowledge graph for the user. The Service and Handler match it with
// errors.Is to map to a 404.
var ErrNotFound = errors.New("knowledge not found")

// ErrForbidden is the authorization sentinel: the caller has no identity.
// Unlike ErrNotFound it never comes from an adapter — the Service mints it.
var ErrForbidden = errors.New("forbidden")

// Graph is a user's semantic memory: its entity nodes and edges.
type Graph struct {
	Nodes []*Node `json:"nodes"`
	Edges []*Edge `json:"edges"`
}

// Get returns the caller's knowledge graph.
func (s *Service) Get(ctx context.Context, nodesQuery, edgesQuery *GraphQuery) (*Graph, error) {
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

	return &Graph{
		Nodes: nodes,
		Edges: edges,
	}, nil
}

// Delete wipes the caller's entire memory. This removes every session too —
// the behaviour is intentional (see CLAUDE.md).
func (s *Service) Delete(ctx context.Context) error {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return ErrForbidden
	}
	if err := s.store.Delete(ctx, userID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("delete memory for user %q: %w", userID, err)
	}
	return nil
}
