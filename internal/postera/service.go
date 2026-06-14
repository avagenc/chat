package postera

import (
	"context"
	"errors"

	apiuser "go.naturallyfunny.dev/api/user"
	"go.naturallyfunny.dev/postera"
)

var (
	ErrNotFound  = errors.New("posterum not found")
	ErrForbidden = errors.New("forbidden")
)

// postarius is the postera v0.18.0 orchestrator surface the gateway consumes.
// It resolves the caller's identity from context (configured via
// WithHumanFromContext in main) to scope ListUpcoming and Cancel.
type postarius interface {
	ListUpcoming(ctx context.Context) ([]postera.Posterum, error)
	Cancel(ctx context.Context, id string) error
}

type Service struct {
	postarius postarius
}

func NewService(p postarius) *Service {
	return &Service{postarius: p}
}

// ListUpcoming returns the authenticated caller's upcoming posterums. Postarius
// scopes the result to the human read from context; we gate on an authenticated
// identity here because the SDK's scoping is filtering, not access control — an
// unscoped context would otherwise expose every caller's posterums.
func (s *Service) ListUpcoming(ctx context.Context) ([]postera.Posterum, error) {
	if _, err := apiuser.IDFromContext(ctx); err != nil {
		return nil, ErrForbidden
	}
	return s.postarius.ListUpcoming(ctx)
}

// Cancel removes the caller's scheduled posterum (both the store row and the
// Cloud Tasks entry, with rollback) via Postarius. We require an authenticated
// identity first; Postarius then scopes the cancel to that human, reporting a
// posterum outside the caller's scope as not found.
func (s *Service) Cancel(ctx context.Context, id string) error {
	if _, err := apiuser.IDFromContext(ctx); err != nil {
		return ErrForbidden
	}

	err := s.postarius.Cancel(ctx, id)
	if errors.Is(err, postera.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
