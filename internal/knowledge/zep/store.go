// Package zep implements the parent knowledge package's Store port on top of
// Zep, mapping Zep's types and not-found errors onto knowledge's. It lives
// beside its contract, like wallet/postgres beside wallet: swapping providers
// means writing a sibling adapter under internal/knowledge, not editing
// knowledge itself. (The agent side has its own Zep surface, adkzep — this
// package only covers semantic memory.)
package zep

import (
	"context"
	"errors"
	"fmt"

	"github.com/avagenc/chat/internal/knowledge"
	zep "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"
)

// Store implements knowledge.Store on top of a Zep client.
type Store struct {
	client *client.Client
}

// NewStore wraps a Zep client as a knowledge.Store.
func NewStore(client *client.Client) *Store {
	return &Store{client: client}
}

var _ knowledge.Store = (*Store)(nil)

func (s *Store) Nodes(ctx context.Context, userID string, query *knowledge.GraphQuery) ([]*knowledge.Node, error) {
	nodes, err := s.client.Graph.Node.GetByUserID(ctx, userID, graphNodesRequest(query))
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, knowledge.ErrNotFound
		}
		return nil, fmt.Errorf("get nodes for user %q: %w", userID, err)
	}
	out := make([]*knowledge.Node, len(nodes))
	for i, n := range nodes {
		out[i] = node(n)
	}
	return out, nil
}

func (s *Store) Edges(ctx context.Context, userID string, query *knowledge.GraphQuery) ([]*knowledge.Edge, error) {
	edges, err := s.client.Graph.Edge.GetByUserID(ctx, userID, graphEdgesRequest(query))
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, knowledge.ErrNotFound
		}
		return nil, fmt.Errorf("get edges for user %q: %w", userID, err)
	}
	out := make([]*knowledge.Edge, len(edges))
	for i, e := range edges {
		out[i] = edge(e)
	}
	return out, nil
}

// Delete maps a memory wipe to Zep's User.Delete, which removes the user and
// all of their data, threads included.
func (s *Store) Delete(ctx context.Context, userID string) error {
	if _, err := s.client.User.Delete(ctx, userID); err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return knowledge.ErrNotFound
		}
		return fmt.Errorf("delete user %q: %w", userID, err)
	}
	return nil
}

func graphNodesRequest(q *knowledge.GraphQuery) *zep.GraphNodesRequest {
	if q == nil {
		return &zep.GraphNodesRequest{}
	}
	return &zep.GraphNodesRequest{
		Limit:      q.Limit,
		UUIDCursor: q.UUIDCursor,
	}
}

func graphEdgesRequest(q *knowledge.GraphQuery) *zep.GraphEdgesRequest {
	if q == nil {
		return &zep.GraphEdgesRequest{}
	}
	return &zep.GraphEdgesRequest{
		Limit:      q.Limit,
		UUIDCursor: q.UUIDCursor,
	}
}

func node(n *zep.EntityNode) *knowledge.Node {
	if n == nil {
		return nil
	}
	return &knowledge.Node{
		Attributes:    n.Attributes,
		CreatedAt:     n.CreatedAt,
		Labels:        n.Labels,
		Name:          n.Name,
		Relevance:     n.Relevance,
		Score:         n.Score,
		SelectionRank: n.SelectionRank,
		Summary:       n.Summary,
		UUID:          n.UUID,
	}
}

func edge(e *zep.EntityEdge) *knowledge.Edge {
	if e == nil {
		return nil
	}
	return &knowledge.Edge{
		Attributes:     e.Attributes,
		CreatedAt:      e.CreatedAt,
		Episodes:       e.Episodes,
		ExpiredAt:      e.ExpiredAt,
		Fact:           e.Fact,
		InvalidAt:      e.InvalidAt,
		Name:           e.Name,
		Relevance:      e.Relevance,
		Scope:          e.Scope,
		Score:          e.Score,
		SelectionRank:  e.SelectionRank,
		SourceNodeUUID: e.SourceNodeUUID,
		TargetNodeUUID: e.TargetNodeUUID,
		UUID:           e.UUID,
		ValidAt:        e.ValidAt,
	}
}
