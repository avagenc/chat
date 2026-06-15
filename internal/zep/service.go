package zep

import (
	"context"
	"errors"
	"fmt"

	zep "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"
	"go.naturallyfunny.dev/api/user"
)

var (
	ErrForbidden = errors.New("forbidden")
	ErrNotFound  = errors.New("not found")
)

type KnowledgeGraph struct {
	Nodes []*zep.EntityNode `json:"nodes"`
	Edges []*zep.EntityEdge `json:"edges"`
}

type Service struct {
	client *client.Client
}

func NewService(client *client.Client) *Service {
	return &Service{
		client: client,
	}
}

func (s *Service) GetMessages(ctx context.Context, threadID string, request *zep.ThreadGetRequest) (*zep.MessageListResponse, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	messages, err := s.client.Thread.Get(ctx, threadID, request)
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get thread %q: %w", threadID, err)
	}

	if messages.UserID == nil || *messages.UserID != userID {
		return nil, ErrForbidden
	}

	return messages, nil
}

func (s *Service) ClearMessages(ctx context.Context, threadID string) (*zep.SuccessResponse, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	messages, err := s.client.Thread.Get(ctx, threadID, &zep.ThreadGetRequest{})
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get thread %q: %w", threadID, err)
	}

	if messages.UserID == nil || *messages.UserID != userID {
		return nil, ErrForbidden
	}

	response, err := s.client.Thread.Delete(ctx, threadID)
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("delete thread %q: %w", threadID, err)
	}

	return response, nil
}

func (s *Service) GetKnowledge(ctx context.Context, nodesReq *zep.GraphNodesRequest, edgesReq *zep.GraphEdgesRequest) (*KnowledgeGraph, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	nodes, err := s.client.Graph.Node.GetByUserID(ctx, userID, nodesReq)
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get knowledge nodes for user %q: %w", userID, err)
	}

	edges, err := s.client.Graph.Edge.GetByUserID(ctx, userID, edgesReq)
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get knowledge edges for user %q: %w", userID, err)
	}

	return &KnowledgeGraph{
		Nodes: nodes,
		Edges: edges,
	}, nil
}

func (s *Service) DeleteKnowledge(ctx context.Context) (*zep.SuccessResponse, error) {
	userID, err := user.IDFromContext(ctx)
	if err != nil {
		return nil, ErrForbidden
	}

	response, err := s.client.User.Delete(ctx, userID)
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("delete knowledge graph for user %q: %w", userID, err)
	}

	return response, nil
}
