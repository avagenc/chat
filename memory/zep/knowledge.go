package zep

import (
	"context"
	"errors"
	"fmt"

	"github.com/avagenc/chat/memory"
	zep "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"
)

// KnowledgeStore implements memory.KnowledgeStore on top of a Zep client.
type KnowledgeStore struct {
	client *client.Client
}

// NewKnowledgeStore wraps a Zep client as a memory.KnowledgeStore.
func NewKnowledgeStore(client *client.Client) *KnowledgeStore {
	return &KnowledgeStore{client: client}
}

var _ memory.KnowledgeStore = (*KnowledgeStore)(nil)

func (s *KnowledgeStore) Nodes(ctx context.Context, userID string, query *memory.GraphQuery) ([]*memory.Node, error) {
	nodes, err := s.client.Graph.Node.GetByUserID(ctx, userID, graphNodesRequest(query))
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, memory.ErrKnowledgeNotFound
		}
		return nil, fmt.Errorf("get nodes for user %q: %w", userID, err)
	}
	out := make([]*memory.Node, len(nodes))
	for i, n := range nodes {
		out[i] = node(n)
	}
	return out, nil
}

func (s *KnowledgeStore) Edges(ctx context.Context, userID string, query *memory.GraphQuery) ([]*memory.Edge, error) {
	edges, err := s.client.Graph.Edge.GetByUserID(ctx, userID, graphEdgesRequest(query))
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, memory.ErrKnowledgeNotFound
		}
		return nil, fmt.Errorf("get edges for user %q: %w", userID, err)
	}
	out := make([]*memory.Edge, len(edges))
	for i, e := range edges {
		out[i] = edge(e)
	}
	return out, nil
}

// Delete maps a memory wipe to Zep's User.Delete, which removes the user and all
// of their data, threads included.
func (s *KnowledgeStore) Delete(ctx context.Context, userID string) error {
	if _, err := s.client.User.Delete(ctx, userID); err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return memory.ErrKnowledgeNotFound
		}
		return fmt.Errorf("delete user %q: %w", userID, err)
	}
	return nil
}

func graphNodesRequest(q *memory.GraphQuery) *zep.GraphNodesRequest {
	if q == nil {
		return &zep.GraphNodesRequest{}
	}
	return &zep.GraphNodesRequest{
		Limit:      q.Limit,
		UUIDCursor: q.UUIDCursor,
	}
}

func graphEdgesRequest(q *memory.GraphQuery) *zep.GraphEdgesRequest {
	if q == nil {
		return &zep.GraphEdgesRequest{}
	}
	return &zep.GraphEdgesRequest{
		Limit:      q.Limit,
		UUIDCursor: q.UUIDCursor,
	}
}

func node(n *zep.EntityNode) *memory.Node {
	if n == nil {
		return nil
	}
	return &memory.Node{
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

func edge(e *zep.EntityEdge) *memory.Edge {
	if e == nil {
		return nil
	}
	return &memory.Edge{
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
