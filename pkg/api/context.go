package api

import (
	"context"
	"errors"
)

type contextKey int

const (
	userIDKey contextKey = iota
)

func NewContextWithUserID(ctx context.Context, userID string) (context.Context, error) {
	if userID == "" {
		return nil, errors.New("user ID cannot be empty")
	}
	return context.WithValue(ctx, userIDKey, userID), nil
}

func GetUserIDFromContext(ctx context.Context) (string, error) {
	val, ok := ctx.Value(userIDKey).(string)
	if !ok || val == "" {
		return "", errors.New("user ID not found in context")
	}
	return val, nil
}
