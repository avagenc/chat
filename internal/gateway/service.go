package gateway

import "context"

type Repository interface {
	IsUserBlocked(ctx context.Context, userID string) (bool, error)
}

type service struct {
	repo Repository
}

func NewService(repo Repository) Service {
	return &service{repo: repo}
}

func (s *service) IsUserBlocked(ctx context.Context, userID string) (bool, error) {
	return s.repo.IsUserBlocked(ctx, userID)
}
