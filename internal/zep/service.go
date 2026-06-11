package zep

import (
	"context"
	"errors"
	"fmt"

	zep "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"
	"go.naturallyfunny.dev/api/user"
)

var ErrForbidden = errors.New("forbidden")

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
		return nil, fmt.Errorf("get messages for thread %q: %w", threadID, err)
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
		return nil, fmt.Errorf("get thread %q for ownership check: %w", threadID, err)
	}

	if messages.UserID == nil || *messages.UserID != userID {
		return nil, ErrForbidden
	}

	response, err := s.client.Thread.Delete(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("clear messages for thread %q: %w", threadID, err)
	}

	return response, nil
}
