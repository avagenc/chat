package zep

import (
	"context"
	"fmt"

	zep "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"
)

type Service struct {
	client *client.Client
}

func NewService(client *client.Client) *Service {
	return &Service{
		client: client,
	}
}

func (s *Service) GetMessages(ctx context.Context, threadID string, request *zep.ThreadGetRequest) (*zep.MessageListResponse, error) {
	messages, err := s.client.Thread.Get(ctx, threadID, request)
	if err != nil {
		return nil, fmt.Errorf("get messages for thread %q: %w", threadID, err)
	}

	return messages, nil
}

func (s *Service) ClearMessages(ctx context.Context, threadID string) (*zep.SuccessResponse, error) {
	response, err := s.client.Thread.Delete(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("clear messages for thread %q: %w", threadID, err)
	}

	return response, nil
}
